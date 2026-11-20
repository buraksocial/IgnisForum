package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/go-chi/chi/v5"
)

const (
	attachmentLimit       = 512 << 10
	attachmentTotalLimit  = 64 << 20
	attachmentCountLimit  = 256
	attachmentRegistryKey = "attachments:v1"
)

type attachmentInfo struct {
	Name string
	Size int
}

func attachmentKey(postID uint64) string { return "attachment:data:" + itoa(postID) }

func validAttachmentName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 160 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, `/\`) && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) == -1
}

func (srv *Server) attachmentRegistry() (map[uint64]attachmentInfo, error) {
	registry := make(map[uint64]attachmentInfo)
	_, err := srv.Store.GetJSON(attachmentRegistryKey, &registry)
	if err != nil || registry == nil || len(registry) > attachmentCountLimit {
		return nil, errors.New("invalid attachment registry")
	}
	for id, info := range registry {
		if id == 0 || info.Size < 1 || info.Size > attachmentLimit || !validAttachmentName(info.Name) {
			return nil, errors.New("invalid attachment registry entry")
		}
	}
	return registry, nil
}

func visibleAttachmentThread(th *domain.Thread) bool {
	return th.DiscussionState == 0 && (th.Status == domain.ThreadOpen || th.Status == domain.ThreadClosed)
}

func (srv *Server) attachmentPost(r *http.Request, me *domain.User) (*domain.Thread, *domain.Post) {
	threadID, postID := parseID(chi.URLParam(r, "threadID")), parseID(chi.URLParam(r, "postID"))
	th, ok := srv.Repos.Threads.Get(threadID)
	if !ok || !visibleAttachmentThread(th) || !srv.can(r, me, perm.ScopeNode, th.NodeID, perm.KeyForumView) {
		return nil, nil
	}
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.ThreadID != th.ID || post.State != 0 {
		return nil, nil
	}
	return th, post
}

func (srv *Server) canManageAttachment(r *http.Request, me *domain.User, th *domain.Thread, post *domain.Post) bool {
	if me == nil || me.State != 0 {
		return false
	}
	if srv.can(r, me, perm.ScopeGlobal, 0, perm.KeyAdmin) {
		return true
	}
	if me.ID != post.UserID || th.Status != domain.ThreadOpen || time.Since(post.Date) > 15*time.Minute {
		return false
	}
	key := perm.KeyForumReply
	if post.ID == th.FirstPostID {
		key = perm.KeyForumThread
	}
	return srv.can(r, me, perm.ScopeNode, th.NodeID, key)
}

func (srv *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if !srv.appearance().Attachments {
		http.NotFound(w, r)
		return
	}
	_, post := srv.attachmentPost(r, srv.me(r))
	if post == nil {
		http.NotFound(w, r)
		return
	}
	srv.attachmentsMu.RLock()
	registry, err := srv.attachmentRegistry()
	info := registry[post.ID]
	var data []byte
	if err == nil && info.Size != 0 {
		data, _ = srv.Store.GetRaw(attachmentKey(post.ID))
	}
	srv.attachmentsMu.RUnlock()
	if info.Size == 0 || len(data) != info.Size {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (srv *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	if !srv.appearance().Attachments {
		http.NotFound(w, r)
		return
	}
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	th, post := srv.attachmentPost(r, me)
	if post == nil {
		http.NotFound(w, r)
		return
	}
	if !srv.canManageAttachment(r, me, th, post) {
		srv.deny(w, r, "Bu mesaja dosya ekleme yetkiniz yok.")
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Dosya çok büyük.", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Geçersiz dosya formu.", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if !csrfOK(r, r.PostForm.Get("csrf")) {
		srv.deny(w, r, "Form doğrulanamadı.")
		return
	}
	if ok, retry := srv.D.RL.Allow("attachment-upload", itoa(me.ID), 5, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	files := r.MultipartForm.File["attachment"]
	if len(files) != 1 || !validAttachmentName(files[0].Filename) || files[0].Size == 0 {
		http.Error(w, "Geçerli bir dosya seçin.", http.StatusBadRequest)
		return
	}
	if files[0].Size > attachmentLimit {
		http.Error(w, "Dosya çok büyük.", http.StatusRequestEntityTooLarge)
		return
	}
	f, err := files[0].Open()
	if err != nil {
		http.Error(w, "Dosya okunamadı.", http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, attachmentLimit+1))
	if err != nil || len(data) > attachmentLimit || int64(len(data)) != files[0].Size {
		http.Error(w, "Dosya okunamadı.", http.StatusBadRequest)
		return
	}
	if status := srv.saveAttachment(post.ID, attachmentInfo{Name: files[0].Filename, Size: len(data)}, data); status != http.StatusSeeOther {
		http.Error(w, "Dosya eklenemedi; mesajın bir eki olabilir veya depolama sınırı dolmuş olabilir.", status)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d/post/%d", th.ID, post.ID), http.StatusSeeOther)
}

func (srv *Server) saveAttachment(postID uint64, info attachmentInfo, data []byte) int {
	srv.attachmentsMu.Lock()
	defer srv.attachmentsMu.Unlock()
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.State != 0 {
		return http.StatusNotFound
	}
	registry, err := srv.attachmentRegistry()
	if err != nil {
		return http.StatusInternalServerError
	}
	if _, exists := registry[postID]; exists {
		return http.StatusConflict
	}
	total := len(data)
	for _, existing := range registry {
		total += existing.Size
	}
	if len(registry) >= attachmentCountLimit || total > attachmentTotalLimit {
		return http.StatusInsufficientStorage
	}
	// Reserve quota before writing bytes. A crash can leave a reservation but
	// cannot leave an uncounted file. The removal action clears either state.
	registry[postID] = info
	if err := srv.Store.PutJSON(attachmentRegistryKey, registry); err != nil {
		return http.StatusInternalServerError
	}
	if err := srv.Store.PutRaw(attachmentKey(postID), data); err != nil {
		log.Printf("attachment write failed for post=%d: %v", postID, err)
		return http.StatusInternalServerError
	}
	return http.StatusSeeOther
}

func (srv *Server) removeAttachment(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.PostForm.Get("csrf")) {
		srv.deny(w, r, "Form doğrulanamadı.")
		return
	}
	th, post := srv.attachmentPost(r, me)
	if post == nil {
		http.NotFound(w, r)
		return
	}
	if !srv.canManageAttachment(r, me, th, post) {
		srv.deny(w, r, "Bu eki kaldırma yetkiniz yok.")
		return
	}
	if ok, retry := srv.D.RL.Allow("attachment-remove", itoa(me.ID), 10, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	status := srv.deleteAttachment(post.ID)
	if status != http.StatusSeeOther {
		http.Error(w, "Dosya eki kaldırılamadı.", status)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%d/post/%d", th.ID, post.ID), status)
}

func (srv *Server) deleteAttachment(postID uint64) int {
	srv.attachmentsMu.Lock()
	defer srv.attachmentsMu.Unlock()
	registry, err := srv.attachmentRegistry()
	if err != nil {
		return http.StatusInternalServerError
	}
	if _, exists := registry[postID]; !exists {
		return http.StatusNotFound
	}
	if _, found := srv.Store.GetRaw(attachmentKey(postID)); found && !srv.Store.Delete(attachmentKey(postID)) {
		return http.StatusInternalServerError
	}
	delete(registry, postID)
	if err := srv.Store.PutJSON(attachmentRegistryKey, registry); err != nil {
		return http.StatusInternalServerError
	}
	return http.StatusSeeOther
}
