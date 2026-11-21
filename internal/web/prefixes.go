package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

type prefixView struct {
	ID          uint64
	Name, Color string
}

func prefixKey(id uint64) string { return fmt.Sprintf("prefix:%d", id) }

func (srv *Server) prefixes() []prefixView {
	out := []prefixView{}
	for id := uint64(1); id <= srv.Store.MaxID(store.TypePrefix); id++ {
		var p domain.ThreadPrefix
		if ok, _ := srv.Store.GetJSON(prefixKey(id), &p); ok {
			out = append(out, prefixView{ID: p.ID, Name: p.Name, Color: p.Color})
		}
	}
	return out
}

func (srv *Server) adminPrefixes(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, srv.uiLocale().Text("admin.prefixes"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
	d.PrefixOptions = srv.prefixes()
	srv.render(w, "admin_prefixes", d)
}

func (srv *Server) saveAdminPrefix(w http.ResponseWriter, r *http.Request) {
	locale := srv.uiLocale()
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, locale.Text("auth.bad_csrf"))
		return
	}
	if ok, retry := srv.D.RL.Allow("admin-prefixes", itoa(admin.ID), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	action := r.FormValue("action")
	id, _ := strconv.ParseUint(r.FormValue("id"), 10, 64)
	var err error
	switch action {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		color := strings.TrimSpace(r.FormValue("color"))
		if len(name) < 2 || len(name) > 40 || (color != "" && !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(color)) {
			err = fmt.Errorf("invalid prefix")
		} else {
			id = srv.Store.NextID()
			srv.Store.MarkID(store.TypePrefix, id)
			err = srv.Store.PutJSON(prefixKey(id), &domain.ThreadPrefix{ID: id, Name: name, Color: color})
		}
	case "delete":
		if ok, _ := srv.Store.GetJSON(prefixKey(id), &domain.ThreadPrefix{}); !ok {
			err = fmt.Errorf("prefix not found")
		} else {
			srv.Store.Delete(prefixKey(id))
		}
	default:
		err = fmt.Errorf("invalid prefix action")
	}
	if err != nil {
		http.Error(w, locale.Text("admin.prefix_save_failed"), http.StatusBadRequest)
		return
	}
	_ = srv.recordAdminAudit(admin.ID, "audit.prefix_"+action, "prefix", id, r.FormValue("name"))
	http.Redirect(w, r, "/admin/prefixes", http.StatusSeeOther)
}

func parsePrefixIDs(raw string) []uint64 {
	out := []uint64{}
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func prefixAllowed(node *domain.Node, id uint64) bool {
	for _, allowed := range node.Options.Prefixes {
		if allowed == id {
			return true
		}
	}
	return false
}
