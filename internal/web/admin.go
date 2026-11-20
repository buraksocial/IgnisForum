package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
)

var adminSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

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

func (srv *Server) adminReports(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "Moderasyon raporları", "admin", []Crumb{{URL: "/admin", Title: "Yönetim"}})
	for _, report := range srv.Repos.OpenReports(0, 100) {
		_, ok := srv.Repos.Posts.Get(report.PostID)
		if !ok {
			continue
		}
		d.Reports = append(d.Reports, adminReportRow{ID: report.ID, PostID: report.PostID, ThreadID: report.ThreadID,
			Reason: report.Reason, Reporter: srv.username(report.ReporterID), CreatedAt: report.CreatedAt})
	}
	srv.render(w, "admin_reports", d)
}

func (srv *Server) resolveReport(w http.ResponseWriter, r *http.Request) {
	admin := srv.requireAdmin(w, r)
	if admin == nil {
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
	if ok, retry := srv.D.RL.Allow("resolve-report", strconv.FormatUint(admin.ID, 10), 60, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	id := parseID(chi.URLParam(r, "reportID"))
	if err := srv.Repos.ResolveReport(id, admin.ID); err != nil {
		http.Error(w, "Rapor bulunamadı.", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
}

func (srv *Server) adminNodes(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "Forum yönetimi", "admin", []Crumb{{URL: "/admin", Title: "Yönetim"}})
	tree, err := srv.Repos.Nodes.LoadTree()
	if err != nil {
		http.Error(w, "forum tree unavailable", http.StatusInternalServerError)
		return
	}
	for _, cat := range tree.Categories {
		if node, ok := srv.Repos.Nodes.Get(cat.NodeID); ok {
			d.AdminNodeRows = append(d.AdminNodeRows, nodeToAdminRow(node))
		}
		d.AdminNodes = append(d.AdminNodes, adminNodeOption{ID: cat.NodeID, Title: cat.Title, Type: "Kategori"})
		for _, forum := range cat.Forums {
			if node, ok := srv.Repos.Nodes.Get(forum.NodeID); ok {
				d.AdminNodeRows = append(d.AdminNodeRows, nodeToAdminRow(node))
			}
		}
	}
	srv.render(w, "admin_nodes", d)
}

func nodeToAdminRow(node *domain.Node) adminNodeRow {
	typeName := "Forum"
	if node.Type == domain.TypeCategory {
		typeName = "Kategori"
	}
	return adminNodeRow{ID: node.ID, Title: node.Title, Slug: node.Slug, Description: node.Description,
		Type: typeName, ParentID: node.ParentID, AllowPolls: node.Options.AllowPolls,
		ThreadCount: node.ThreadCount, PostCount: node.PostCount}
}

func (srv *Server) saveAdminNode(w http.ResponseWriter, r *http.Request) {
	me := srv.requireAdmin(w, r)
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
	if ok, retry := srv.D.RL.Allow("admin-nodes", strconv.FormatUint(me.ID, 10), 30, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	title, slug := strings.TrimSpace(r.FormValue("title")), strings.Trim(strings.ToLower(strings.TrimSpace(r.FormValue("slug"))), "-")
	description := strings.TrimSpace(r.FormValue("description"))
	action := r.FormValue("action")
	switch action {
	case "create-category":
		if !validNodeForm(title, slug, description) {
			http.Error(w, "Başlık, slug veya açıklama geçersiz.", http.StatusBadRequest)
			return
		}
		_, err := srv.Repos.Nodes.CreateCategory(title, slug, description)
		if err != nil {
			http.Error(w, "Kategori oluşturulamadı.", http.StatusInternalServerError)
			return
		}
	case "create-forum":
		if !validNodeForm(title, slug, description) {
			http.Error(w, "Başlık, slug veya açıklama geçersiz.", http.StatusBadRequest)
			return
		}
		parent, err := strconv.ParseUint(r.FormValue("parent_id"), 10, 64)
		parentNode, ok := srv.Repos.Nodes.Get(parent)
		if err != nil || !ok || parentNode.Type != domain.TypeCategory {
			http.Error(w, "Üst kategori geçersiz.", http.StatusBadRequest)
			return
		}
		_, err = srv.Repos.Nodes.CreateForum(parent, title, slug, description, r.FormValue("allow_polls") == "1")
		if err != nil {
			http.Error(w, "Forum oluşturulamadı.", http.StatusInternalServerError)
			return
		}
	case "update":
		if !validNodeForm(title, slug, description) {
			http.Error(w, "Başlık, slug veya açıklama geçersiz.", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "Düğüm kimliği geçersiz.", http.StatusBadRequest)
			return
		}
		node, ok := srv.Repos.Nodes.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		options := node.Options
		options.AllowPolls = node.Type == domain.TypeForum && r.FormValue("allow_polls") == "1"
		parent := uint64(0)
		if node.Type == domain.TypeForum {
			parent, err = strconv.ParseUint(r.FormValue("parent_id"), 10, 64)
			parentNode, found := srv.Repos.Nodes.Get(parent)
			if err != nil || !found || parentNode.Type != domain.TypeCategory {
				http.Error(w, "Üst kategori geçersiz.", http.StatusBadRequest)
				return
			}
		}
		if err := srv.Repos.Nodes.Update(id, title, slug, description, options); err != nil {
			http.Error(w, "Düğüm güncellenemedi.", http.StatusInternalServerError)
			return
		}
		if node.ParentID != parent {
			if err := srv.Repos.Nodes.MoveNode(id, parent); err != nil {
				http.Error(w, "Düğüm taşınamadı.", http.StatusInternalServerError)
				return
			}
			srv.D.Perms.Invalidate()
		}
	case "delete-empty":
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "Düğüm kimliği geçersiz.", http.StatusBadRequest)
			return
		}
		if err := srv.Repos.Nodes.DeleteEmpty(id); err != nil {
			http.Error(w, "Yalnızca boş forum veya kategoriler silinebilir.", http.StatusConflict)
			return
		}
	default:
		http.Error(w, "İşlem geçersiz.", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/nodes", http.StatusSeeOther)
}

func validNodeForm(title, slug, description string) bool {
	return len(title) >= 2 && len(title) <= 100 && len(description) <= 1000 && adminSlugPattern.MatchString(slug) && len(slug) <= 100
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
