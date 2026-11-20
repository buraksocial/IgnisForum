// Package web wires chi routes to handlers that render SSR templates from the
// repo layer. Performance contract (§10): the home page is ONE Get (forum_tree)
// + one node-blob Get per forum + one windowed ZPage for latest threads; the
// thread view is one windowed ZOldest + batched JSON gets. No handler may
// enumerate a ZSET wholesale (use store.ZPage/ZOldest, never the ByScore
// materialisation path).
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/buraksocial/IgnisForum/internal/auth"
	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/buraksocial/IgnisForum/internal/plugin"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// Version is stamped by main via ldflags.
var Version = "dev"

// Deps carries the M2 services into the web layer.
type Deps struct {
	Auth      *auth.Service
	Users     *auth.UserRepo
	RL        *auth.RateLimiter
	Perms     *perm.Engine
	Secure    bool // cookies carry the Secure flag (behind TLS, M6)
	ThemeDir  string
	AvatarDir string
	Plugins   *plugin.Manager
}

// Server holds shared dependencies for all handlers.
type Server struct {
	Store    *store.Store
	Repos    *repo.Repos
	D        Deps
	themes   *ThemeManager
	tmpl     *template.Template
	presence memberPresence
}

// New builds the router with the M2 surface (content + auth). `assets` is the
// embedded theme FS (templates/ + styles/thred/style.css) from the module root
// assets package.
func New(s *store.Store, r *repo.Repos, d Deps, assets embed.FS) chi.Router {
	themes, err := LoadThemeManager(assets, d.ThemeDir)
	if err != nil {
		panic("web: load themes: " + err.Error())
	}
	srv := &Server{
		Store:  s,
		Repos:  r,
		D:      d,
		themes: themes,
		tmpl:   loadTemplates(assets),
	}
	return buildRouter(srv)
}

// buildRouter wires middleware + routes; factored out so tests can hold a
// *Server directly.
func buildRouter(srv *Server) chi.Router {

	rr := chi.NewRouter()
	rr.Use(chimw.Recoverer)
	rr.Use(chimw.RequestID)
	rr.Use(chimw.RealIP)
	rr.Use(srv.sessionMW) // resolves ignis_sid → context (one KV Get per request)

	rr.Get("/healthz", srv.healthz)
	rr.Get("/uploads/avatars/{filename}", srv.avatarFile)
	rr.Get("/s/{theme}/style.css", srv.serveStyle)
	rr.Get("/theme/{themeID}", srv.selectTheme)

	rr.Get("/", srv.home)
	rr.Get("/latest", srv.latest)
	rr.Get("/notifications", srv.notificationsPage)
	rr.Post("/notifications/{notificationID}/read", srv.markNotificationRead)
	rr.Get("/search", srv.search)
	rr.Get("/members/{userID}", srv.memberProfile)
	rr.Post("/members/{userID}/profile", srv.saveMemberProfile)
	rr.Get("/tags/{slug}", srv.tagThreads)
	rr.Get("/f/{nodeID}", srv.forum)
	rr.Get("/f/{nodeID}/new", srv.newThreadForm)
	rr.Post("/f/{nodeID}/new", srv.createThread)
	rr.Get("/t/{threadID}", srv.thread)
	rr.Post("/t/{threadID}/follow", srv.toggleThreadFollow)
	rr.Post("/t/{threadID}/poll/vote", srv.votePoll)
	rr.Get("/t/{threadID}/post/{postID}", srv.postRedirect)
	rr.Post("/t/{threadID}/reply", srv.reply)
	rr.Post("/t/{threadID}/post/{postID}/edit", srv.editPost)
	rr.Post("/t/{threadID}/post/{postID}/delete", srv.deletePost)
	rr.Post("/t/{threadID}/post/{postID}/report", srv.reportPost)
	rr.Post("/t/{threadID}/post/{postID}/reaction", srv.react)

	rr.Get("/login", srv.loginForm)
	rr.Post("/login", srv.loginSubmit)
	rr.Get("/register", srv.registerForm)
	rr.Post("/register", srv.registerSubmit)
	rr.Post("/logout", srv.logout)
	rr.Get("/admin", srv.adminHome)
	rr.Get("/admin/appearance", srv.adminAppearance)
	rr.Post("/admin/appearance", srv.saveAppearance)
	rr.Get("/media/preview", srv.mediaPreview)
	rr.Get("/admin/nodes", srv.adminNodes)
	rr.Post("/admin/nodes", srv.saveAdminNode)
	rr.Get("/admin/reports", srv.adminReports)
	rr.Post("/admin/reports/{reportID}/resolve", srv.resolveReport)
	rr.Get("/admin/permissions", srv.adminPermissions)
	rr.Post("/admin/permissions", srv.saveAdminPermissions)

	rr.NotFound(srv.notFound)
	return rr
}

// --- session context ---

type ctxKey int

const sessionCtx ctxKey = 1

// sessionMW resolves the ignis_sid cookie to a live session and stores it in
// the request context. It is exactly one KV Get for signed-in users and zero
// for guests (cookie missing → skip).
func (srv *Server) sessionMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // forms are small
			r.ParseMultipartForm(0)
		}
		if c, err := r.Cookie("ignis_sid"); err == nil && c.Value != "" {
			if sess := srv.D.Auth.GetSession(c.Value); sess != nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionCtx, sess)))
				return
			}
			// Stale/expired cookie: clear it so the browser stops sending it.
			http.SetCookie(w, &http.Cookie{Name: "ignis_sid", Value: "", Path: "/", MaxAge: -1})
		}
		next.ServeHTTP(w, r)
	})
}

// sessOf returns the request's session (nil = guest).
func sessOf(r *http.Request) *auth.Session {
	s, _ := r.Context().Value(sessionCtx).(*auth.Session)
	return s
}

// me resolves the signed-in user blob (nil for guests).
func (srv *Server) me(r *http.Request) *domain.User {
	s := sessOf(r)
	if s == nil {
		return nil
	}
	u, _ := srv.D.Users.GetByID(s.UserID)
	return u
}

// --- view models ---

type Crumb struct{ URL, Title string }

type pageData struct {
	Appearance appearanceSettings
	Extensions []plugin.Status
	Sidebar    sidebarView
	PreviewURL string
	Title      string
	Nav        string
	Crumbs     []Crumb
	Rebuilding bool
	Version    string
	Now        time.Time

	// M2 identity: Me is nil for guests; CSRF is the session token every
	// form must submit.
	Me                  *domain.User
	Sess                *auth.Session
	CSRF                string
	Admin               bool
	UnreadNotifications int
	Flashes             []string

	// home / latest
	Cats          []catView
	LatestThreads []threadRow

	// forum
	Node       *domain.Node
	Sort       string
	Page       int
	TotalPages int
	PageWindow []int
	Threads    []threadRow
	CanThread  bool

	// thread
	Thread     *domain.Thread
	Posts      []postRow
	CanReply   bool
	Following  bool
	ReplyDraft string

	// search
	Query string
	Hits  []threadRow
	HaveQ bool

	// auth forms
	FormError     string
	Remember      bool
	RedirectBack  string
	ThemeID       string
	ThemeVersion  string
	ThemeOptions  []ThemeOption
	ThemeBack     string
	PluginNotices []string

	// administration
	AdminNodes          []adminNodeOption
	AdminNodeID         uint64
	AdminScope          string
	AdminGroups         []adminGroupView
	AdminPermissionRows []adminPermissionRow
	AdminNodeRows       []adminNodeRow
	Reports             []adminReportRow
	Profile             *domain.User
	ProfilePosts        []profilePostRow
	ProfilePostCount    int
	Tags                []tagView
	Poll                *pollView
	Notifications       []notificationView
}

type profilePostRow struct {
	ThreadID    uint64
	ThreadTitle string
	Date        time.Time
	Message     template.HTML
}
type tagView struct {
	Slug, Title string
	ThreadCount int
}
type pollView struct {
	Question                    string
	Multiple, CanVote, HasVoted bool
	Options                     []pollOptionView
}
type pollOptionView struct {
	Index, Votes int
	Text         string
	Selected     bool
}
type notificationView struct {
	ID                 uint64
	Actor, ThreadTitle string
	ThreadID           uint64
	CreatedAt          time.Time
	Read               bool
}

type adminNodeOption struct {
	ID    uint64
	Title string
	Type  string
}
type adminNodeRow struct {
	ID                       uint64
	Title, Slug, Description string
	Type                     string
	ParentID                 uint64
	AllowPolls               bool
	ThreadCount, PostCount   int
}
type adminReportRow struct {
	ID, PostID, ThreadID uint64
	Reason, Reporter     string
	CreatedAt            time.Time
}
type adminGroupView struct {
	ID   uint64
	Name string
}
type adminPermissionCell struct{ Name, Value string }
type adminPermissionRow struct {
	Key, Label string
	Cells      []adminPermissionCell
}

type catView struct {
	Title  string
	Forums []forumView
}

type forumView struct {
	ID            uint64
	Title         string
	Slug          string
	Description   string
	ThreadCount   int
	PostCount     int
	LastPostURL   string
	LastPostTitle string
	LastPoster    string
	LastPostTime  time.Time
}

type threadRow struct {
	ID            uint64
	Title         string
	Author        string
	Date          time.Time
	ReplyCount    int
	ViewCount     int
	LastPostID    uint64
	LastPostTitle string
	LastPoster    string
	LastPostTime  time.Time
}

type postRow struct {
	Media     []mediaItem
	ID        uint64
	Number    int
	UserID    uint64
	Author    string
	AvatarURL string
	Rank      string
	Date      time.Time
	Rendered  template.HTML
	Edited    bool
	CanEdit   bool
	CanDelete bool
	Raw       string
	CanReport bool
	Reactions []reactionView
}

type reactionView struct {
	Type, Label string
	Count       int
	Selected    bool
}

var reactionLabels = map[string]string{"like": "👍 Beğen", "love": "❤️ Sev", "laugh": "😂 Komik", "sad": "😢 Üzücü", "wow": "😮 Şaşırtıcı"}

// --- base helpers ---

// base builds the render model. Me/CSRF/Admin are resolved for every page from
// the session context (zero extra reads for guests, one Get for users).
func (srv *Server) base(r *http.Request, title, nav string, crumbs []Crumb) *pageData {
	d := &pageData{
		Appearance: srv.appearance(),
		Title:      title, Nav: nav, Crumbs: crumbs,
		Rebuilding: srv.Store.Rebuilding(),
		Version:    Version, Now: time.Now(),
		Sess: sessOf(r),
	}
	d.ThemeBack = safeNext(r.URL.RequestURI())
	if d.Sess != nil {
		d.CSRF = d.Sess.CSRF
		d.Me = srv.me(r)
		if d.Me == nil {
			// Session points at a deleted user: drop both.
			srv.D.Auth.DestroySession(d.Sess.SID)
			d.Sess, d.CSRF = nil, ""
		} else {
			d.Admin = srv.can(r, d.Me, perm.ScopeGlobal, 0, perm.KeyAdmin)
			if d.Appearance.Presence && d.Me.State == 0 {
				srv.presence.touch(d.Me.ID, d.Now)
			}
			d.UnreadNotifications = srv.Repos.Notifications.UnreadCount(d.Me.ID)
		}
	}
	themeID := ""
	if d.Me != nil {
		themeID = d.Me.Theme
	}
	if themeID == "" {
		if cookie, err := r.Cookie("ignis_theme"); err == nil {
			themeID = cookie.Value
		}
	}
	if theme, _, ok := srv.themes.Get(themeID); ok {
		d.ThemeID, d.ThemeVersion = theme.ID, theme.Version
	} else if theme, _, ok := srv.themes.Get(d.Appearance.DefaultTheme); ok {
		d.ThemeID, d.ThemeVersion = theme.ID, theme.Version
	}
	d.ThemeOptions = srv.themes.Options()
	return d
}

func (srv *Server) notificationsPage(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	d := srv.base(r, "Bildirimler", "notifications", []Crumb{{Title: "Bildirimler"}})
	for _, id := range srv.Repos.Notifications.List(me.ID, 0, 50) {
		item, ok := srv.Repos.Notifications.Get(id)
		if !ok {
			continue
		}
		thread, ok := srv.Repos.Threads.Get(item.ThreadID)
		if !ok || !srv.can(r, me, perm.ScopeNode, thread.NodeID, perm.KeyForumView) {
			continue
		}
		d.Notifications = append(d.Notifications, notificationView{ID: item.ID, Actor: srv.username(item.ActorID), ThreadTitle: thread.Title, ThreadID: thread.ID, CreatedAt: item.CreatedAt, Read: item.Read})
	}
	srv.render(w, "notifications", d)
}

func (srv *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	id := parseID(chi.URLParam(r, "notificationID"))
	if err := srv.Repos.Notifications.MarkRead(me.ID, id); err != nil {
		srv.notFound(w, r)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// can is the permission check seam (Effective is cached in perm.Engine).
func (srv *Server) can(r *http.Request, u *domain.User, ctype string, cid uint64, key string) bool {
	var uid uint64
	var groups []uint64
	if u != nil {
		uid, groups = u.ID, u.GroupIDs
	}
	return srv.D.Perms.Effective(uid, groups, ctype, cid, key)
}

// csrfOK validates the posted token against the session. Guests have no
// session at all → every POST from them must be rejected (the handlers
// redirect to login before this even matters, but defence in depth).
func csrfOK(r *http.Request, token string) bool {
	return auth.ValidateCSRF(sessOf(r), token)
}

// requireAuth gates write endpoints: guests are bounced to /login?next=... .
func (srv *Server) requireAuth(w http.ResponseWriter, r *http.Request) *domain.User {
	if u := srv.me(r); u != nil {
		return u
	}
	target := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
	if r.Method != http.MethodGet {
		target = "/login"
	}
	http.Redirect(w, r, target, http.StatusFound)
	return nil
}

// deny renders a permission-denied page (logged in but lacks the grant).
func (srv *Server) deny(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusForbidden)
	d := srv.base(r, "İzin yok", "", nil)
	d.FormError = msg
	srv.render(w, "error", d)
}

// tooMany renders the 429 state with Retry-After so clients back off honestly.
func (srv *Server) tooMany(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	if retry < time.Second {
		retry = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
	w.WriteHeader(http.StatusTooManyRequests)
	d := srv.base(r, "Çok fazla deneme", "", nil)
	d.FormError = fmt.Sprintf("Çok fazla deneme yaptınız. Lütfen %d saniye sonra tekrar deneyin.", int(retry.Seconds()))
	srv.render(w, "error", d)
}

func (srv *Server) render(w http.ResponseWriter, name string, data *pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := srv.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
	}
}

func parseID(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }
func itoa(v uint64) string    { return strconv.FormatUint(v, 10) }

func parseInt(r *http.Request, name string, def int) int {
	if v := r.URL.Query().Get(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

const pageSize = 20

func pageWindow(page, totalPages int) []int {
	if totalPages < 2 {
		return nil
	}
	lo, hi := page-3, page+3
	if lo < 1 {
		lo = 1
	}
	if hi > totalPages {
		hi = totalPages
	}
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

func pageCount(total, size int) int {
	if total <= 0 {
		return 1
	}
	return (total + size - 1) / size
}

// --- infra handlers ---

func (srv *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	st := srv.Store.Stats()
	st["version"] = Version
	st["rebuilding"] = srv.Store.Rebuilding()
	b, _ := json.Marshal(st)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func (srv *Server) serveStyle(w http.ResponseWriter, r *http.Request) {
	theme, css, ok := srv.themes.Get(chi.URLParam(r, "theme"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+theme.Version+`"`)
	_, _ = w.Write(css)
}

func (srv *Server) selectTheme(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "themeID")
	if _, _, ok := srv.themes.Get(id); !ok {
		http.NotFound(w, r)
		return
	}
	if me := srv.me(r); me != nil {
		if err := srv.D.Users.SetTheme(me.ID, id); err != nil {
			http.Error(w, "theme preference could not be saved", http.StatusInternalServerError)
			return
		}
	} else {
		http.SetCookie(w, &http.Cookie{Name: "ignis_theme", Value: id, Path: "/", MaxAge: 365 * 24 * 60 * 60,
			HttpOnly: true, Secure: srv.D.Secure, SameSite: http.SameSiteLaxMode})
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

func (srv *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	srv.render(w, "notfound", srv.base(r, "Bulunamadı", "", nil))
}

// --- content handlers ---

func (srv *Server) home(w http.ResponseWriter, r *http.Request) {
	tree, err := srv.Repos.Nodes.LoadTree()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d := srv.base(r, "IgnisForum — Forum", "home", nil)
	d.Cats = srv.buildCats(r, d.Me, tree)
	d.Sidebar = srv.homeSidebar(r, d)
	d.LatestThreads = srv.threadsFromIDs(srv.Store.ZPage(repo.IdxLatestThreads, 0, 10), d.Me)
	if srv.D.Plugins != nil {
		results, err := srv.D.Plugins.RunHook(r.Context(), "home.decorate", map[string]any{"version": Version, "categories": len(d.Cats)})
		if err != nil {
			log.Printf("plugin hook home.decorate: %v", err)
		}
		for _, result := range results {
			var response struct {
				Notices []string `json:"notices"`
			}
			if json.Unmarshal(result, &response) != nil {
				continue
			}
			for _, notice := range response.Notices {
				notice = strings.TrimSpace(notice)
				if notice != "" && len(notice) <= 240 && len(d.PluginNotices) < 5 {
					d.PluginNotices = append(d.PluginNotices, notice)
				}
			}
		}
	}
	srv.render(w, "home", d)
}

// buildCats turns the slim tree blob into a render model. One node-blob Get per
// forum supplies counts + last-post card; the engine hot cache keeps this cheap.
// Forums the caller lacks forum_view on are skipped entirely (XenForo behaviour).
func (srv *Server) buildCats(r *http.Request, me *domain.User, tree *repo.Tree) []catView {
	out := make([]catView, 0, len(tree.Categories))
	for _, c := range tree.Categories {
		cv := catView{Title: c.Title}
		for _, f := range c.Forums {
			if !srv.can(r, me, perm.ScopeNode, f.NodeID, perm.KeyForumView) {
				continue
			}
			fv := forumView{ID: f.NodeID, Title: f.Title, Slug: f.Slug}
			if node, ok := srv.Repos.Nodes.Get(f.NodeID); ok {
				fv.Description = node.Description
				fv.ThreadCount = node.ThreadCount
				fv.PostCount = node.PostCount
				if node.LastPost != nil {
					fv.LastPostTime = node.LastPost.T
					fv.LastPoster = srv.username(node.LastPost.UserID)
					if post, ok := srv.Repos.Posts.Get(node.LastPost.PostID); ok {
						if th, ok := srv.Repos.Threads.Get(post.ThreadID); ok {
							fv.LastPostTitle = th.Title
							fv.LastPostURL = fmt.Sprintf("/t/%d/post/%d", th.ID, post.ID)
						}
					}
				}
			}
			cv.Forums = append(cv.Forums, fv)
		}
		if len(cv.Forums) > 0 {
			out = append(out, cv)
		}
	}
	return out
}

func (srv *Server) latest(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Yeni içerikler", "latest", nil)
	d.LatestThreads = srv.threadsFromIDs(srv.Store.ZPage(repo.IdxLatestThreads, 0, 50), d.Me)
	srv.render(w, "latest", d)
}

func (srv *Server) forum(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	node, ok := srv.Repos.Nodes.Get(nodeID)
	if !ok {
		srv.notFound(w, r)
		return
	}
	d := srv.base(r, node.Title, "home", []Crumb{{URL: "/f/" + itoa(nodeID), Title: node.Title}})
	if !srv.can(r, d.Me, perm.ScopeNode, nodeID, perm.KeyForumView) {
		if d.Me == nil {
			srv.requireAuth(w, r) // guests → login (with next=)
			return
		}
		srv.deny(w, r, "Bu forumu görüntüleme izniniz yok.")
		return
	}
	sortKey := r.URL.Query().Get("sort")
	switch sortKey {
	case "new", "posts", "views", "last", "":
	default:
		sortKey = "last"
	}
	page := parseInt(r, "p", 1)
	ids := srv.Repos.Threads.List(nodeID, sortKey, (page-1)*pageSize, pageSize)

	d.Node = node
	d.Sort = sortKey
	d.Threads = srv.threadsFromIDs(ids, d.Me)
	d.Page = page
	d.TotalPages = pageCount(srv.Store.ZCard(repo.NodeThreadsKey(nodeID)), pageSize)
	d.PageWindow = pageWindow(page, d.TotalPages)
	d.CanThread = srv.can(r, d.Me, perm.ScopeNode, nodeID, perm.KeyForumThread)
	srv.render(w, "forum", d)
}

func (srv *Server) thread(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	th, ok := srv.Repos.Threads.Get(threadID)
	if !ok || th.Status == domain.ThreadDeleted {
		srv.notFound(w, r)
		return
	}
	d := srv.base(r, th.Title, "home", []Crumb{{URL: "/f/" + itoa(th.NodeID), Title: srv.nodeName(th.NodeID)}})
	if !srv.can(r, d.Me, perm.ScopeNode, th.NodeID, perm.KeyForumView) {
		if d.Me == nil {
			srv.requireAuth(w, r)
			return
		}
		srv.deny(w, r, "Bu konuyu görüntüleme izniniz yok.")
		return
	}
	// View counter: write primary + one in-node index score update (M1 accepts
	// the per-node O(n) shift; see §3 perf note).
	_ = srv.Repos.Threads.IncrementViews(threadID)

	page := parseInt(r, "p", 1)
	offset := (page - 1) * pageSize
	postIDs := srv.Repos.Posts.List(threadID, offset, pageSize)
	for _, tagID := range th.TagIDs {
		if tag, found := srv.Repos.Tags.ByID(tagID); found {
			d.Tags = append(d.Tags, tagView{Slug: tag.Slug, Title: tag.Title, ThreadCount: tag.ThreadCount})
		}
	}
	if poll, found := srv.Repos.Polls.Get(threadID); found && th.PollID == threadID {
		view := &pollView{Question: poll.Question, Multiple: poll.Multiple, CanVote: d.Me != nil && th.Status == domain.ThreadOpen}
		selected := make(map[int]bool)
		if d.Me != nil {
			choices, voted := srv.Repos.Polls.UserVote(threadID, d.Me.ID)
			view.HasVoted = voted
			view.CanVote = view.CanVote && !voted
			for _, choice := range choices {
				selected[choice] = true
			}
		}
		for i, option := range poll.Options {
			view.Options = append(view.Options, pollOptionView{Index: i, Text: option.Text, Votes: option.Votes, Selected: selected[i]})
		}
		d.Poll = view
	}

	rows := make([]postRow, 0, len(postIDs))
	showMedia := d.Appearance.Media
	userCache := make(map[uint64]*domain.User)
	lastReadPostNumber := uint64(0)
	for i, pid := range postIDs {
		post, ok := srv.Repos.Posts.Get(pid)
		if !ok || post.State == 2 {
			continue
		}
		author, avatarURL := "Misafir", ""
		if post.UserID != 0 {
			user, found := userCache[post.UserID]
			if !found && srv.D.Users != nil {
				user, _ = srv.D.Users.GetByID(post.UserID)
				userCache[post.UserID] = user
			}
			if user != nil {
				author = user.Username
				if validAvatarName(user.AvatarPath) {
					avatarURL = "/uploads/avatars/" + user.AvatarPath
				}
			} else {
				author = "kullanıcı #" + itoa(post.UserID)
			}
		}
		row := postRow{
			ID: post.ID, Number: offset + i + 1, UserID: post.UserID,
			Author: author, AvatarURL: avatarURL, Rank: "Üye", Date: post.Date,
			Rendered: template.HTML(post.MessageRendered),
			Edited:   post.EditCount > 0,
			Raw:      post.MessageRaw,
		}
		if showMedia && post.State == 0 {
			row.Media = postMedia(post.MessageRaw)
		}
		if d.Me != nil {
			row.CanEdit = d.Me.ID == post.UserID && time.Since(post.Date) <= 15*time.Minute || srv.can(r, d.Me, perm.ScopeGlobal, 0, perm.KeyAdmin)
			row.CanDelete = post.ID != th.FirstPostID && (d.Me.ID == post.UserID && time.Since(post.Date) <= 15*time.Minute || srv.can(r, d.Me, perm.ScopeGlobal, 0, perm.KeyAdmin))
			row.CanReport = d.Me.ID != post.UserID
		}
		myReaction := ""
		if d.Me != nil {
			myReaction, _ = srv.Repos.Reactions.Get(post.ID, d.Me.ID)
		}
		for _, kind := range repo.ReactionTypes {
			row.Reactions = append(row.Reactions, reactionView{Type: kind, Label: reactionLabels[kind], Count: post.ReactionCounts[kind], Selected: kind == myReaction})
		}
		rows = append(rows, row)
		if row.Number > int(lastReadPostNumber) {
			lastReadPostNumber = uint64(row.Number)
		}
	}
	if d.Me != nil {
		state, _ := srv.Repos.GetThreadUserState(threadID, d.Me.ID)
		if state == nil {
			state = &domain.ThreadUserState{UserID: d.Me.ID, ThreadID: threadID}
		}
		d.Following = state.Subscription == "follow"
		if lastReadPostNumber > state.LastReadPostNumber {
			state.LastReadPostNumber = lastReadPostNumber
			state.LastReadAt = time.Now().UTC()
			if err := srv.Repos.PutThreadUserState(state); err != nil {
				log.Printf("thread read state update failed user=%d thread=%d: %v", d.Me.ID, threadID, err)
			}
		}
	}

	d.CanReply = th.Status == domain.ThreadOpen && srv.can(r, d.Me, perm.ScopeNode, th.NodeID, perm.KeyForumReply)
	if d.CanReply {
		if quoteID := parseID(r.URL.Query().Get("quote")); quoteID != 0 {
			if quoted, ok := srv.Repos.Posts.Get(quoteID); ok && quoted.ThreadID == threadID && quoted.State == 0 {
				quoteText := quoted.MessageRaw
				if utf8.RuneCountInString(quoteText) > 3000 {
					runes := []rune(quoteText)
					quoteText = string(runes[:3000])
				}
				d.ReplyDraft = "[quote]\n" + quoteText + "\n[/quote]\n\n"
			}
		}
	}
	d.Thread = th
	d.Posts = rows
	d.Page = page
	d.TotalPages = pageCount(srv.Store.ZCard(repo.ThreadPostsKey(threadID)), pageSize)
	d.PageWindow = pageWindow(page, d.TotalPages)
	srv.render(w, "thread", d)
}

func (srv *Server) toggleThreadFollow(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	thread, ok := srv.Repos.Threads.Get(threadID)
	if !ok || thread.Status == domain.ThreadDeleted {
		srv.notFound(w, r)
		return
	}
	if !srv.can(r, me, perm.ScopeNode, thread.NodeID, perm.KeyForumView) {
		srv.deny(w, r, "Bu konuyu takip etme izniniz yok.")
		return
	}
	if ok, retry := srv.D.RL.Allow("thread-follow", itoa(me.ID), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	state, _ := srv.Repos.GetThreadUserState(threadID, me.ID)
	if state == nil {
		state = &domain.ThreadUserState{UserID: me.ID, ThreadID: threadID}
	}
	if state.Subscription == "follow" {
		state.Subscription = ""
	} else {
		state.Subscription = "follow"
	}
	if err := srv.Repos.PutThreadUserState(state); err != nil {
		http.Error(w, "Konu takibi güncellenemedi.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/t/"+itoa(threadID), http.StatusSeeOther)
}

// postRedirect resolves a /t/{tid}/post/{pid} deep link to the owning page by
// counting members at-or-before the post's score (bounded by thread size —
// ZCountUpTo materialises the range, which is fine for one thread's index).
func (srv *Server) postRedirect(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	postID := parseID(chi.URLParam(r, "postID"))
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.ThreadID != threadID {
		srv.notFound(w, r)
		return
	}
	rank := srv.Store.ZCountUpTo(repo.ThreadPostsKey(threadID), float64(post.Date.Unix()))
	page := (rank-1)/pageSize + 1
	if page < 1 {
		page = 1
	}
	http.Redirect(w, r, fmt.Sprintf("/t/%s?p=%d#post-%s", itoa(threadID), page, itoa(postID)), http.StatusFound)
}

func (srv *Server) reply(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	// Auth gate FIRST (guests bounce to login), then CSRF, then permissions.
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		srv.notFound(w, r)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "Oturum doğrulaması başarısız (CSRF). Sayfayı yenileyip tekrar deneyin.")
		return
	}
	th, ok := srv.Repos.Threads.Get(threadID)
	if !ok {
		srv.notFound(w, r)
		return
	}
	if !srv.can(r, me, perm.ScopeNode, th.NodeID, perm.KeyForumReply) {
		srv.deny(w, r, "Bu foruma yanıt yazma izniniz yok.")
		return
	}
	// Anti-flood: 30s window, max 5 replies per user (§ rate-limit spec).
	if ok, retry := srv.D.RL.Allow("reply", itoa(me.ID), 5, 30*time.Second); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	msg := strings.TrimSpace(r.FormValue("message"))
	if msg == "" {
		http.Redirect(w, r, "/t/"+itoa(threadID), http.StatusSeeOther)
		return
	}
	post, err := srv.Repos.Posts.Create(threadID, me.ID, msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rank := srv.Store.ZCountUpTo(repo.ThreadPostsKey(threadID), float64(post.Date.Unix()))
	page := (rank-1)/pageSize + 1
	http.Redirect(w, r, fmt.Sprintf("/t/%s?p=%d#post-%s", itoa(threadID), page, itoa(post.ID)), http.StatusSeeOther)
}

func (srv *Server) editPost(w http.ResponseWriter, r *http.Request) {
	threadID, postID := parseID(chi.URLParam(r, "threadID")), parseID(chi.URLParam(r, "postID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	post, ok := srv.Repos.Posts.Get(postID)
	_, threadOK := srv.Repos.Threads.Get(threadID)
	if !ok || !threadOK || post.ThreadID != threadID || post.State != 0 {
		srv.notFound(w, r)
		return
	}
	isAdmin := srv.can(r, me, perm.ScopeGlobal, 0, perm.KeyAdmin)
	if (me.ID != post.UserID || time.Since(post.Date) > 15*time.Minute) && !isAdmin {
		srv.deny(w, r, "Bu mesajı düzenleme yetkiniz yok.")
		return
	}
	if ok, retry := srv.D.RL.Allow("edit-post", itoa(me.ID), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" || len(message) > 20000 {
		http.Error(w, "Mesaj 1 ile 20000 karakter arasında olmalıdır.", http.StatusBadRequest)
		return
	}
	if _, err := srv.Repos.Posts.Edit(postID, message); err != nil {
		http.Error(w, "Mesaj güncellenemedi.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/t/"+itoa(threadID)+"#post-"+itoa(postID), http.StatusSeeOther)
}

func (srv *Server) reportPost(w http.ResponseWriter, r *http.Request) {
	threadID, postID := parseID(chi.URLParam(r, "threadID")), parseID(chi.URLParam(r, "postID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.ThreadID != threadID || post.State != 0 {
		srv.notFound(w, r)
		return
	}
	if post.UserID == me.ID {
		srv.deny(w, r, "Kendi mesajınızı raporlayamazsınız.")
		return
	}
	if ok, retry := srv.D.RL.Allow("report", itoa(me.ID), 10, time.Hour); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	report, err := srv.Repos.CreateReport(postID, me.ID, r.FormValue("reason"))
	if err != nil {
		http.Error(w, "Rapor gerekçesi 3-500 karakter olmalıdır.", http.StatusBadRequest)
		return
	}
	_ = report
	http.Redirect(w, r, "/t/"+itoa(threadID)+"#post-"+itoa(postID), http.StatusSeeOther)
}

func (srv *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	threadID, postID := parseID(chi.URLParam(r, "threadID")), parseID(chi.URLParam(r, "postID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	post, ok := srv.Repos.Posts.Get(postID)
	thread, threadOK := srv.Repos.Threads.Get(threadID)
	if !ok || !threadOK || post.ThreadID != threadID || post.State != 0 {
		srv.notFound(w, r)
		return
	}
	if post.ID == thread.FirstPostID {
		srv.deny(w, r, "Konunun ilk mesajı silinemez.")
		return
	}
	isAdmin := srv.can(r, me, perm.ScopeGlobal, 0, perm.KeyAdmin)
	if !isAdmin && (post.UserID != me.ID || time.Since(post.Date) > 15*time.Minute) {
		srv.deny(w, r, "Bu mesajı silme yetkiniz yok.")
		return
	}
	if allowed, retry := srv.D.RL.Allow("delete-post", itoa(me.ID), 10, time.Minute); !allowed {
		srv.tooMany(w, r, retry)
		return
	}
	if err := srv.Repos.Posts.DeleteReply(postID); err != nil {
		http.Error(w, "Mesaj silinemedi.", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/t/"+itoa(threadID), http.StatusSeeOther)
}

func (srv *Server) react(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	postID := parseID(chi.URLParam(r, "postID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		srv.notFound(w, r)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "Oturum doğrulaması başarısız (CSRF). Sayfayı yenileyip tekrar deneyin.")
		return
	}
	post, ok := srv.Repos.Posts.Get(postID)
	if !ok || post.ThreadID != threadID || post.State != 0 {
		srv.notFound(w, r)
		return
	}
	thread, ok := srv.Repos.Threads.Get(threadID)
	if !ok || thread.Status != domain.ThreadOpen || !srv.can(r, me, perm.ScopeNode, thread.NodeID, perm.KeyForumReply) {
		srv.deny(w, r, "Bu mesaja tepki verme izniniz yok.")
		return
	}
	if ok, retry := srv.D.RL.Allow("reaction", itoa(me.ID), 30, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	if _, err := srv.Repos.Reactions.Toggle(postID, me.ID, r.FormValue("reaction")); err != nil {
		srv.deny(w, r, "Geçersiz tepki isteği.")
		return
	}
	back := "/t/" + itoa(threadID) + "#post-" + itoa(postID)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (srv *Server) newThreadForm(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	node, ok := srv.Repos.Nodes.Get(nodeID)
	if !ok {
		srv.notFound(w, r)
		return
	}
	d := srv.base(r, "Yeni Konu", "home", []Crumb{
		{URL: "/f/" + itoa(nodeID), Title: node.Title}, {Title: "Yeni Konu"},
	})
	if srv.requireAuth(w, r) == nil {
		return
	}
	if !srv.can(r, d.Me, perm.ScopeNode, nodeID, perm.KeyForumThread) {
		srv.deny(w, r, "Bu forumda konu açma izniniz yok.")
		return
	}
	d.Node = node
	srv.render(w, "newthread", d)
}

func (srv *Server) createThread(w http.ResponseWriter, r *http.Request) {
	nodeID := parseID(chi.URLParam(r, "nodeID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		srv.notFound(w, r)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "Oturum doğrulaması başarısız (CSRF). Sayfayı yenileyip tekrar deneyin.")
		return
	}
	if !srv.can(r, me, perm.ScopeNode, nodeID, perm.KeyForumThread) {
		srv.deny(w, r, "Bu forumda konu açma izniniz yok.")
		return
	}
	if ok, retry := srv.D.RL.Allow("thread", itoa(me.ID), 3, 60*time.Second); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	msg := strings.TrimSpace(r.FormValue("message"))
	pollQuestion := strings.TrimSpace(r.FormValue("poll_question"))
	pollOptions := parsePollOptions(r.FormValue("poll_options"))
	pollMultiple := r.FormValue("poll_multiple") == "1"
	var tagNames []string
	for _, rawTags := range r.Form["tags"] {
		tagNames = append(tagNames, strings.Split(rawTags, ",")...)
	}
	if len(tagNames) > 5 {
		http.Error(w, "Bir konuya en fazla 5 etiket eklenebilir.", http.StatusBadRequest)
		return
	}
	for _, tagName := range tagNames {
		if utf8.RuneCountInString(strings.TrimSpace(tagName)) > 32 {
			http.Error(w, "Etiketler en fazla 32 karakter olabilir.", http.StatusBadRequest)
			return
		}
	}
	if title == "" || msg == "" {
		http.Error(w, "başlık ve mesaj gerekli", http.StatusBadRequest)
		return
	}
	if pollQuestion != "" {
		node, ok := srv.Repos.Nodes.Get(nodeID)
		if !ok || !node.Options.AllowPolls {
			http.Error(w, "Bu forumda anket açılamaz.", http.StatusBadRequest)
			return
		}
		if !validPollInput(pollQuestion, pollOptions) {
			http.Error(w, "Anket 3-150 karakterlik bir soru ve 2-10 geçerli seçenek içermelidir.", http.StatusBadRequest)
			return
		}
	} else if len(pollOptions) > 0 || pollMultiple {
		http.Error(w, "Anket seçenekleri için soru da gereklidir.", http.StatusBadRequest)
		return
	}
	th, _, err := srv.Repos.Threads.Create(nodeID, me.ID, title, msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := srv.Repos.Tags.ApplyNames(th.ID, tagNames); err != nil {
		http.Error(w, "Etiketler kaydedilemedi.", http.StatusInternalServerError)
		return
	}
	if pollQuestion != "" {
		if err := srv.Repos.Polls.Create(th.ID, pollQuestion, pollOptions, pollMultiple); err != nil {
			http.Error(w, "Anket oluşturulamadı.", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/t/"+itoa(th.ID), http.StatusSeeOther)
}

func parsePollOptions(raw string) []string {
	var options []string
	for _, line := range strings.Split(raw, "\n") {
		if option := strings.TrimSpace(line); option != "" {
			options = append(options, option)
		}
	}
	return options
}

func validPollInput(question string, options []string) bool {
	if n := utf8.RuneCountInString(question); n < 3 || n > 150 || len(options) < 2 || len(options) > 10 {
		return false
	}
	seen := map[string]bool{}
	for _, option := range options {
		option = strings.TrimSpace(option)
		if n := utf8.RuneCountInString(option); n == 0 || n > 100 {
			return false
		}
		key := strings.ToLower(option)
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func (srv *Server) votePoll(w http.ResponseWriter, r *http.Request) {
	threadID := parseID(chi.URLParam(r, "threadID"))
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Geçersiz form.", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	thread, ok := srv.Repos.Threads.Get(threadID)
	if !ok || !srv.can(r, me, perm.ScopeNode, thread.NodeID, perm.KeyForumView) {
		srv.notFound(w, r)
		return
	}
	var choices []int
	for _, raw := range r.Form["option"] {
		choice, err := strconv.Atoi(raw)
		if err != nil {
			srv.deny(w, r, "Geçersiz anket seçeneği.")
			return
		}
		choices = append(choices, choice)
	}
	if allowed, retry := srv.D.RL.Allow("poll-vote", itoa(me.ID), 20, time.Minute); !allowed {
		srv.tooMany(w, r, retry)
		return
	}
	if err := srv.Repos.Polls.Vote(threadID, me.ID, choices); err != nil {
		srv.deny(w, r, "Oy kaydedilemedi: "+err.Error())
		return
	}
	http.Redirect(w, r, "/t/"+itoa(threadID), http.StatusSeeOther)
}

func (srv *Server) tagThreads(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	tag, ok := srv.Repos.Tags.BySlug(slug)
	if !ok {
		srv.notFound(w, r)
		return
	}
	d := srv.base(r, "Etiket: "+tag.Title, "latest", []Crumb{{Title: "Etiketler"}, {Title: tag.Title}})
	d.Tags = []tagView{{Slug: tag.Slug, Title: tag.Title, ThreadCount: tag.ThreadCount}}
	var visibleIDs []uint64
	for _, id := range srv.Repos.Tags.List(slug, 0, 100) {
		thread, found := srv.Repos.Threads.Get(id)
		if !found || thread.Status == domain.ThreadDeleted || thread.DiscussionState != 0 || !srv.can(r, d.Me, perm.ScopeNode, thread.NodeID, perm.KeyForumView) {
			continue
		}
		visibleIDs = append(visibleIDs, id)
	}
	d.LatestThreads = srv.threadsFromIDs(visibleIDs, d.Me)
	srv.render(w, "tag", d)
}

func (srv *Server) memberProfile(w http.ResponseWriter, r *http.Request) {
	userID := parseID(chi.URLParam(r, "userID"))
	user, ok := srv.D.Users.GetByID(userID)
	if !ok || user.State != 0 {
		srv.notFound(w, r)
		return
	}
	d := srv.base(r, user.Username+" profili", "", []Crumb{{Title: "Üyeler"}, {Title: user.Username}})
	d.Profile = user
	d.ProfilePostCount = srv.Repos.Posts.CountByUser(userID)
	for _, id := range srv.Repos.Posts.RecentByUser(userID, 10) {
		post, found := srv.Repos.Posts.Get(id)
		if !found || post.State != 0 {
			continue
		}
		thread, found := srv.Repos.Threads.Get(post.ThreadID)
		if !found || thread.Status == domain.ThreadDeleted || !srv.can(r, d.Me, perm.ScopeNode, thread.NodeID, perm.KeyForumView) {
			continue
		}
		d.ProfilePosts = append(d.ProfilePosts, profilePostRow{ThreadID: thread.ID, ThreadTitle: thread.Title, Date: post.Date, Message: template.HTML(post.MessageRendered)})
	}
	srv.render(w, "member", d)
}

func (srv *Server) saveMemberProfile(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAuth(w, r)
	if me == nil {
		return
	}
	userID := parseID(chi.URLParam(r, "userID"))
	if userID != me.ID {
		srv.deny(w, r, "Yalnızca kendi profilinizi düzenleyebilirsiniz.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Geçersiz form.", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "CSRF doğrulaması başarısız.")
		return
	}
	title, signature := strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("signature"))
	if utf8.RuneCountInString(title) > 60 || utf8.RuneCountInString(signature) > 500 {
		http.Error(w, "Başlık en fazla 60, imza en fazla 500 karakter olabilir.", http.StatusBadRequest)
		return
	}
	if allowed, retry := srv.D.RL.Allow("edit-profile", itoa(me.ID), 5, time.Minute); !allowed {
		srv.tooMany(w, r, retry)
		return
	}
	if err := srv.D.Users.UpdateProfile(me.ID, title, signature); err != nil {
		http.Error(w, "Profil kaydedilemedi.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/members/"+itoa(me.ID), http.StatusSeeOther)
}

func (srv *Server) search(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Arama", "search", nil)
	if !srv.can(r, d.Me, perm.ScopeGlobal, 0, perm.KeySearch) {
		if d.Me == nil {
			srv.requireAuth(w, r)
			return
		}
		srv.deny(w, r, "Arama yapma izniniz yok.")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d.Query = q
	d.HaveQ = q != ""
	if q != "" {
		// 10 searches / 30s per user bucket (user id or guest IP hash).
		bucket := "g:" + auth.HashIP(r.RemoteAddr)
		if d.Me != nil {
			bucket = "u:" + itoa(d.Me.ID)
		}
		if ok, retry := srv.D.RL.Allow("search", bucket, 10, 30*time.Second); !ok {
			srv.tooMany(w, r, retry)
			return
		}
		hits := srv.Store.FTSearch(q, 50)
		seen := make(map[uint64]struct{}, len(hits))
		threads := make([]uint64, 0, len(hits))
		for _, h := range hits {
			if !strings.HasPrefix(h.DocID, "post:") {
				continue
			}
			pid := parseID(strings.TrimPrefix(h.DocID, "post:"))
			post, ok := srv.Repos.Posts.Get(pid)
			if !ok || post.State != 0 {
				continue
			}
			if _, dup := seen[post.ThreadID]; dup {
				continue
			}
			seen[post.ThreadID] = struct{}{}
			threads = append(threads, post.ThreadID)
		}
		d.Hits = srv.threadsFromIDs(threads, d.Me)
	}
	srv.render(w, "search", d)
}

// --- auth handlers ---

const maxBody = 1 << 20 // 1MB — forms are small; reject anything larger early

func (srv *Server) setSessionCookie(w http.ResponseWriter, sess *auth.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     "ignis_sid",
		Value:    sess.SID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   srv.D.Secure,
		MaxAge:   int(time.Until(sess.ExpiresAt).Seconds()),
	})
}

// issuePreCSRF creates a short-lived double-submit token for login/register,
// where there is not yet an authenticated session to bind the token to.
func (srv *Server) issuePreCSRF(w http.ResponseWriter) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: "ignis_pre_csrf", Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: srv.D.Secure, MaxAge: 600,
	})
	return token, nil
}

func validPreCSRF(r *http.Request) bool {
	c, err := r.Cookie("ignis_pre_csrf")
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.FormValue("csrf"))) == 1
}

func (srv *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Giriş", "", nil)
	if d.Me != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if d.CSRF, _ = srv.issuePreCSRF(w); d.CSRF == "" {
		http.Error(w, "csrf token unavailable", http.StatusInternalServerError)
		return
	}
	d.RedirectBack = safeNext(r.URL.Query().Get("next"))
	srv.render(w, "login", d)
}

// safeNext keeps the login redirect on-site (no open redirect).
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func (srv *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Giriş", "", nil)
	d.Remember = r.FormValue("remember") != ""
	d.RedirectBack = safeNext(r.FormValue("next"))
	fail := func(status int, msg string) {
		d.FormError = msg
		w.WriteHeader(status)
		srv.render(w, "login", d)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil || r.ContentLength > maxBody {
		fail(http.StatusBadRequest, "İstek çözümlenemedi.")
		return
	}
	if !validPreCSRF(r) {
		fail(http.StatusForbidden, "Oturum doğrulaması başarısız (CSRF). Formu yenileyip tekrar deneyin.")
		return
	}
	ident := strings.TrimSpace(r.FormValue("username"))
	pw := r.FormValue("password")

	// Two buckets: per-identifier and per-IP (XenForo-style pairing).
	ipBucket := auth.HashIP(r.RemoteAddr)
	okA, retryA := srv.D.RL.Allow("login", "ip:"+ipBucket, 10, 10*time.Minute)
	okB, retryB := srv.D.RL.Allow("login", "id:"+strings.ToLower(ident), 10, 10*time.Minute)
	if !okA || !okB {
		retry := retryA
		if okA || retryB > retry {
			retry = retryB
		}
		srv.tooMany(w, r, retry)
		return
	}

	usr, sess, err := srv.D.Users.Login(ident, pw, d.Remember, r.RemoteAddr)
	if err != nil {
		fail(http.StatusUnauthorized, "Geçersiz kullanıcı adı veya şifre.")
		return
	}
	srv.setSessionCookie(w, sess)
	http.Redirect(w, r, d.RedirectBack, http.StatusFound)
	_ = usr
}

func (srv *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Kayıt", "", nil)
	if d.Me != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !srv.can(r, nil, perm.ScopeGlobal, 0, perm.KeyUserRegister) {
		srv.deny(w, r, "Kayıt şu anda kapalı.")
		return
	}
	if d.CSRF, _ = srv.issuePreCSRF(w); d.CSRF == "" {
		http.Error(w, "csrf token unavailable", http.StatusInternalServerError)
		return
	}
	srv.render(w, "register", d)
}

func (srv *Server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	d := srv.base(r, "Kayıt", "", nil)
	fail := func(status int, msg string) {
		d.FormError = msg
		w.WriteHeader(status)
		srv.render(w, "register", d)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil || r.ContentLength > maxBody {
		fail(http.StatusBadRequest, "İstek çözümlenemedi.")
		return
	}
	if !validPreCSRF(r) {
		fail(http.StatusForbidden, "Oturum doğrulaması başarısız (CSRF). Formu yenileyip tekrar deneyin.")
		return
	}
	if !srv.can(r, nil, perm.ScopeGlobal, 0, perm.KeyUserRegister) {
		srv.deny(w, r, "Kayıt şu anda kapalı.")
		return
	}
	// 3 registrations / hour per IP — public endpoint, must be throttled.
	if ok, retry := srv.D.RL.Allow("register", auth.HashIP(r.RemoteAddr), 3, time.Hour); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	usr, err := srv.D.Users.Register(
		r.FormValue("username"), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrUsernameTaken):
			fail(http.StatusConflict, "Bu kullanıcı adı zaten alınmış.")
		case errors.Is(err, auth.ErrEmailTaken):
			fail(http.StatusConflict, "Bu e-posta ile başka bir hesap var.")
		default:
			fail(http.StatusBadRequest, err.Error())
		}
		return
	}
	// Auto-login on success (XenForo behaviour).
	sess, err := srv.D.Auth.CreateSession(usr.ID, true, r.RemoteAddr)
	if err != nil {
		fail(http.StatusInternalServerError, "Oturum kurulamadı.")
		return
	}
	srv.setSessionCookie(w, sess)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (srv *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil && csrfOK(r, r.FormValue("csrf")) {
		if s := sessOf(r); s != nil {
			srv.D.Auth.DestroySession(s.SID)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "ignis_sid", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- shared view helpers ---

func (srv *Server) threadsFromIDs(ids []uint64, me *domain.User) []threadRow {
	rows := make([]threadRow, 0, len(ids))
	for _, id := range ids {
		th, ok := srv.Repos.Threads.Get(id)
		if !ok || th.Status == domain.ThreadDeleted || th.DiscussionState != 0 || !srv.can(nil, me, perm.ScopeNode, th.NodeID, perm.KeyForumView) {
			continue
		}
		replies := th.PostCount - 1
		if replies < 0 {
			replies = 0
		}
		row := threadRow{
			ID: th.ID, Title: th.Title, Date: th.Date,
			ReplyCount: replies, ViewCount: th.ViewCount,
			Author: srv.username(th.UserID),
		}
		if th.LastPost != nil {
			row.LastPostID = th.LastPost.PostID
			row.LastPostTime = th.LastPost.T
			row.LastPoster = srv.username(th.LastPost.UserID)
			row.LastPostTitle = th.Title
		}
		rows = append(rows, row)
	}
	return rows
}

func (srv *Server) username(uid uint64) string {
	// Real user blobs (M2). Missing user (deleted) still renders a fallback.
	if uid == 0 {
		return "Misafir"
	}
	if name := srv.D.Users.Username(uid); name != "" {
		return name
	}
	return "kullanıcı #" + itoa(uid)
}

func (srv *Server) avatarFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "filename")
	if srv.D.AvatarDir == "" || !validAvatarName(name) || filepath.Base(name) != name {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(srv.D.AvatarDir, name)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > 8<<20 {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		http.NotFound(w, r)
		return
	}
	contentType := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".gif": "image/gif"}[filepath.Ext(name)]
	if contentType == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func validAvatarName(name string) bool {
	if len(name) < 6 || len(name) > 64 || name[0] != 'u' {
		return false
	}
	dot := strings.LastIndexByte(name, '.')
	if dot < 2 {
		return false
	}
	if _, err := strconv.ParseUint(name[1:dot], 10, 64); err != nil {
		return false
	}
	switch name[dot:] {
	case ".png", ".jpg", ".gif":
		return true
	default:
		return false
	}
}

func (srv *Server) nodeName(id uint64) string {
	if n, ok := srv.Repos.Nodes.Get(id); ok {
		return n.Title
	}
	return "Forum"
}

// --- templates ---

func loadTemplates(assets embed.FS) *template.Template {
	entries, err := assets.ReadDir("templates")
	if err != nil {
		panic("templates dir missing from embed: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tmpl") {
			names = append(names, "templates/"+e.Name())
		}
	}
	sort.Strings(names)
	t := template.New("").Funcs(tmplFuncs)
	return template.Must(t.ParseFS(assets, names...))
}

var tmplFuncs = template.FuncMap{
	"ago": humanTime,
	"first": func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return "?"
		}
		r := []rune(s)
		return strings.ToUpper(string(r[0]))
	},
	"sub": func(a, b int) int { return a - b },
	"add": func(a, b int) int { return a + b },
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "az önce"
	case d < time.Hour:
		return fmt.Sprintf("%d dk önce", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d sa önce", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d gün önce", int(d.Hours()/24))
	default:
		return t.Format("02.01.2006")
	}
}
