package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
)

var adminGroups = []adminGroupView{
	{ID: perm.GroupGuest, Name: "Misafir"},
	{ID: perm.GroupMember, Name: "Üye"},
	{ID: perm.GroupAdmin, Name: "Yönetici"},
}

var permissionLabels = map[string]string{
	perm.KeyAccess:       "Siteye erişim",
	perm.KeyForumView:    "Forumları görüntüleme",
	perm.KeyForumThread:  "Konu açma",
	perm.KeyForumReply:   "Yanıt yazma",
	perm.KeySearch:       "Arama",
	perm.KeyUserRegister: "Kayıt olma",
	perm.KeyAdmin:        "Yönetim paneli",
}

func (srv *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *domain.User {
	me := srv.requireAuth(w, r)
	if me == nil {
		return nil
	}
	if !srv.can(r, me, perm.ScopeGlobal, 0, perm.KeyAdmin) {
		srv.deny(w, r, "Bu işlem için yönetici izni gerekiyor.")
		return nil
	}
	return me
}

func (srv *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "Yönetim", "admin", nil)
	srv.render(w, "admin", d)
}

func (srv *Server) adminPermissions(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "İzin yönetimi", "admin", []Crumb{{URL: "/admin", Title: "Yönetim"}})
	d.AdminScope = r.URL.Query().Get("scope")
	if d.AdminScope == "" {
		d.AdminScope = perm.ScopeGlobal
	}
	if d.AdminScope != perm.ScopeGlobal && d.AdminScope != perm.ScopeNode {
		http.Error(w, "invalid permission scope", http.StatusBadRequest)
		return
	}
	d.AdminNodeID, _ = strconv.ParseUint(r.URL.Query().Get("node"), 10, 64)
	if d.AdminScope == perm.ScopeNode {
		if _, ok := srv.Repos.Nodes.Get(d.AdminNodeID); !ok || d.AdminNodeID == 0 {
			http.Error(w, "forum node not found", http.StatusNotFound)
			return
		}
	}
	srv.fillAdminPermissionRows(d)
	srv.render(w, "admin_permissions", d)
}

func (srv *Server) fillAdminPermissionRows(d *pageData) {
	d.AdminGroups = adminGroups
	if tree, err := srv.Repos.Nodes.LoadTree(); err == nil {
		for _, cat := range tree.Categories {
			d.AdminNodes = append(d.AdminNodes, adminNodeOption{ID: cat.NodeID, Title: cat.Title, Type: "Kategori"})
			for _, forum := range cat.Forums {
				d.AdminNodes = append(d.AdminNodes, adminNodeOption{ID: forum.NodeID, Title: "↳ " + forum.Title, Type: "Forum"})
			}
		}
	}
	ctype, cid := d.AdminScope, d.AdminNodeID
	if ctype == perm.ScopeGlobal {
		cid = 0
	}
	for _, key := range perm.AllKeys {
		row := adminPermissionRow{Key: key, Label: permissionLabels[key]}
		for _, group := range adminGroups {
			value := "inherit"
			if grants := srv.D.Perms.GetGrant(group.ID, ctype, cid); grants != nil {
				if allowed, exists := grants[key]; exists {
					value = "deny"
					if allowed {
						value = "allow"
					}
				}
			}
			row.Cells = append(row.Cells, adminPermissionCell{Name: "g" + strconv.FormatUint(group.ID, 10) + "_" + key, Value: value})
		}
		d.AdminPermissionRows = append(d.AdminPermissionRows, row)
	}
}

func (srv *Server) saveAdminPermissions(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAdmin(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, "Oturum doğrulaması başarısız (CSRF). Formu yenileyip tekrar deneyin.")
		return
	}
	scope := r.FormValue("scope")
	cid := uint64(0)
	if scope == perm.ScopeNode {
		parsed, err := strconv.ParseUint(r.FormValue("node"), 10, 64)
		if err != nil || parsed == 0 {
			http.Error(w, "invalid node", http.StatusBadRequest)
			return
		}
		if _, ok := srv.Repos.Nodes.Get(parsed); !ok {
			http.Error(w, "forum node not found", http.StatusNotFound)
			return
		}
		cid = parsed
	} else if scope != perm.ScopeGlobal {
		http.Error(w, "invalid permission scope", http.StatusBadRequest)
		return
	}
	if ok, retry := srv.D.RL.Allow("admin-permissions", strconv.FormatUint(me.ID, 10), 30, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	for _, group := range adminGroups {
		grants := srv.D.Perms.GetGrant(group.ID, scope, cid)
		if grants == nil {
			grants = make(map[string]bool)
		}
		for _, key := range perm.AllKeys {
			value := r.FormValue("g" + strconv.FormatUint(group.ID, 10) + "_" + key)
			switch value {
			case "inherit":
				delete(grants, key)
			case "allow":
				grants[key] = true
			case "deny":
				grants[key] = false
			default:
				http.Error(w, "invalid permission value", http.StatusBadRequest)
				return
			}
		}
		// Keep administrators from accidentally removing their own global gate.
		if group.ID == perm.GroupAdmin && scope == perm.ScopeGlobal {
			grants[perm.KeyAdmin] = true
			grants[perm.KeyAccess] = true
		}
		if len(grants) == 0 {
			grants = nil
		}
		if err := srv.D.Perms.SetGrant(group.ID, scope, cid, grants); err != nil {
			http.Error(w, "permission update failed", http.StatusInternalServerError)
			return
		}
	}
	location := "/admin/permissions?scope=" + scope
	if scope == perm.ScopeNode {
		location += "&node=" + strconv.FormatUint(cid, 10)
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
