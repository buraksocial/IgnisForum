package web

import (
	"fmt"
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
	{ID: perm.GroupGuest, Name: "group.guest"},
	{ID: perm.GroupMember, Name: "group.member"},
	{ID: perm.GroupAdmin, Name: "group.admin"},
}

func (srv *Server) availableGroups() []adminGroupView {
	groups := make([]adminGroupView, 0)
	for _, group := range srv.D.Perms.ListGroups() {
		name := group.Name
		if group.ID == perm.GroupGuest {
			name = "group.guest"
		}
		if group.ID == perm.GroupMember {
			name = "group.member"
		}
		if group.ID == perm.GroupAdmin {
			name = "group.admin"
		}
		groups = append(groups, adminGroupView{ID: group.ID, Name: name, Description: group.Description, System: group.System})
	}
	return groups
}

var permissionLabels = map[string]string{
	perm.KeyAccess:       "permission.access",
	perm.KeyForumView:    "permission.view",
	perm.KeyForumThread:  "permission.thread",
	perm.KeyForumReply:   "permission.reply",
	perm.KeySearch:       "permission.search",
	perm.KeyUserRegister: "permission.register",
	perm.KeyAdmin:        "permission.admin",
}

func (srv *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *domain.User {
	me := srv.requireAuth(w, r)
	if me == nil {
		return nil
	}
	if !srv.can(r, me, perm.ScopeGlobal, 0, perm.KeyAdmin) {
		srv.deny(w, r, srv.uiLocale().Text("admin.denied"))
		return nil
	}
	return me
}

func (srv *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, srv.uiLocale().Text("admin.home"), "admin", nil)
	srv.render(w, "admin", d)
}

func (srv *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	page := parseInt(r, "p", 1)
	if page > 100000 {
		page = 100000
	}
	users, total := srv.D.Users.List((page-1)*pageSize, pageSize)
	d := srv.base(r, srv.uiLocale().Text("admin.users"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
	d.AdminUserPage, d.AdminUserTotal = page, total
	d.TotalPages = pageCount(total, pageSize)
	d.PageWindow = pageWindow(page, d.TotalPages)
	for _, user := range users {
		d.AdminUsers = append(d.AdminUsers, adminUserRow{ID: user.ID, Username: user.Username, State: user.State,
			Groups: append([]uint64(nil), user.GroupIDs...), JoinedAt: user.JoinedAt, LastSeen: user.LastSeen})
	}
	srv.render(w, "admin_users", d)
}

func (srv *Server) saveAdminUser(w http.ResponseWriter, r *http.Request) {
	locale := srv.uiLocale()
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, locale.Text("auth.bad_csrf"))
		return
	}
	if ok, retry := srv.D.RL.Allow("admin-users", strconv.FormatUint(admin.ID, 10), 30, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	targetID := parseID(chi.URLParam(r, "userID"))
	target, ok := srv.D.Users.GetByID(targetID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	action := r.FormValue("action")
	switch action {
	case "suspend", "activate":
		if targetID == admin.ID && action == "suspend" {
			http.Error(w, locale.Text("admin.user_self_suspend"), http.StatusConflict)
			return
		}
		target.State = 0
		if action == "suspend" {
			target.State = 1
		}
		if err := srv.Store.PutJSON("user:"+strconv.FormatUint(target.ID, 10), target); err != nil {
			http.Error(w, locale.Text("admin.user_save_failed"), http.StatusInternalServerError)
			return
		}
		actionName := "audit.user_activate"
		if action == "suspend" {
			actionName = "audit.user_suspend"
		}
		if err := srv.recordAdminAudit(admin.ID, actionName, "user", target.ID, target.Username); err != nil {
			http.Error(w, locale.Text("admin.user_save_failed"), http.StatusInternalServerError)
			return
		}
	case "groups":
		groups := make([]uint64, 0, 2)
		for _, raw := range strings.Split(r.FormValue("groups"), ",") {
			id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				http.Error(w, locale.Text("admin.user_bad_groups"), http.StatusBadRequest)
				return
			}
			if _, exists := srv.D.Perms.GetGroup(id); !exists {
				http.Error(w, locale.Text("admin.user_bad_groups"), http.StatusBadRequest)
				return
			}
			found := false
			for _, existing := range groups {
				if existing == id {
					found = true
					break
				}
			}
			if !found {
				groups = append(groups, id)
			}
		}
		if len(groups) == 0 {
			groups = []uint64{perm.GroupMember}
		}
		if targetID == admin.ID && !containsGroup(groups, perm.GroupAdmin) {
			http.Error(w, locale.Text("admin.user_self_demote"), http.StatusConflict)
			return
		}
		target.GroupIDs = groups
		if err := srv.Store.PutJSON("user:"+strconv.FormatUint(target.ID, 10), target); err != nil {
			http.Error(w, locale.Text("admin.user_save_failed"), http.StatusInternalServerError)
			return
		}
		if err := srv.recordAdminAudit(admin.ID, "audit.user_groups", "user", target.ID, target.Username); err != nil {
			http.Error(w, locale.Text("admin.user_save_failed"), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, locale.Text("admin.user_bad_action"), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/users?p="+strconv.Itoa(parseInt(r, "p", 1)), http.StatusSeeOther)
}

func (srv *Server) adminGroupsPage(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, srv.uiLocale().Text("admin.groups"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
	d.AdminGroups = srv.availableGroups()
	srv.render(w, "admin_groups", d)
}

func (srv *Server) saveAdminGroup(w http.ResponseWriter, r *http.Request) {
	locale := srv.uiLocale()
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, locale.Text("auth.bad_csrf"))
		return
	}
	if ok, retry := srv.D.RL.Allow("admin-groups", strconv.FormatUint(admin.ID, 10), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	action := r.FormValue("action")
	id, _ := strconv.ParseUint(r.FormValue("id"), 10, 64)
	var err error
	switch action {
	case "create":
		var group *domain.Group
		group, err = srv.D.Perms.CreateGroup(r.FormValue("name"), r.FormValue("description"))
		if err == nil {
			err = srv.recordAdminAudit(admin.ID, "audit.group_create", "group", group.ID, group.Name)
		}
	case "update":
		err = srv.D.Perms.UpdateGroup(id, r.FormValue("name"), r.FormValue("description"))
		if err == nil {
			err = srv.recordAdminAudit(admin.ID, "audit.group_update", "group", id, r.FormValue("name"))
		}
	case "delete":
		group, exists := srv.D.Perms.GetGroup(id)
		if !exists {
			err = fmt.Errorf("group not found")
		} else if group.System {
			err = perm.ErrSystemGroup
		} else if srv.groupInUse(id) {
			err = fmt.Errorf("group in use")
		} else {
			err = srv.D.Perms.DeleteGroup(id)
			if err == nil {
				err = srv.recordAdminAudit(admin.ID, "audit.group_delete", "group", id, group.Name)
			}
		}
	default:
		err = fmt.Errorf("invalid group action")
	}
	if err != nil {
		http.Error(w, locale.Text("admin.group_save_failed"), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (srv *Server) groupInUse(id uint64) bool {
	page := 0
	for {
		users, total := srv.D.Users.List(page*pageSize, pageSize)
		for _, user := range users {
			if containsGroup(user.GroupIDs, id) {
				return true
			}
		}
		page++
		if page*pageSize >= total || len(users) == 0 {
			return false
		}
	}
}

func containsGroup(groups []uint64, want uint64) bool {
	for _, group := range groups {
		if group == want {
			return true
		}
	}
	return false
}

func (srv *Server) adminReports(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, srv.uiLocale().Text("report.heading"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
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
	locale := srv.uiLocale()
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, locale.Text("auth.bad_form"), http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, locale.Text("auth.bad_csrf"))
		return
	}
	if ok, retry := srv.D.RL.Allow("resolve-report", strconv.FormatUint(admin.ID, 10), 60, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	id := parseID(chi.URLParam(r, "reportID"))
	if err := srv.Repos.ResolveReport(id, admin.ID); err != nil {
		http.Error(w, locale.Text("report.missing"), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
}

func (srv *Server) adminNodes(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, srv.uiLocale().Text("admin.structure"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
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
	ids := make([]string, len(node.Options.Prefixes))
	for i, id := range node.Options.Prefixes {
		ids[i] = strconv.FormatUint(id, 10)
	}
	return adminNodeRow{ID: node.ID, Title: node.Title, Slug: node.Slug, Description: node.Description, Prefixes: strings.Join(ids, ","),
		Type: typeName, ParentID: node.ParentID, AllowPolls: node.Options.AllowPolls,
		ThreadCount: node.ThreadCount, PostCount: node.PostCount}
}

func (srv *Server) saveAdminNode(w http.ResponseWriter, r *http.Request) {
	locale := srv.uiLocale()
	me := srv.requireAdmin(w, r)
	if me == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, locale.Text("auth.bad_form"), http.StatusBadRequest)
		return
	}
	if !csrfOK(r, r.FormValue("csrf")) {
		srv.deny(w, r, locale.Text("auth.bad_csrf"))
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
			http.Error(w, locale.Text("node.bad_fields"), http.StatusBadRequest)
			return
		}
		_, err := srv.Repos.Nodes.CreateCategory(title, slug, description)
		if err != nil {
			http.Error(w, locale.Text("node.category_failed"), http.StatusInternalServerError)
			return
		}
	case "create-forum":
		if !validNodeForm(title, slug, description) {
			http.Error(w, locale.Text("node.bad_fields"), http.StatusBadRequest)
			return
		}
		parent, err := strconv.ParseUint(r.FormValue("parent_id"), 10, 64)
		parentNode, ok := srv.Repos.Nodes.Get(parent)
		if err != nil || !ok || parentNode.Type != domain.TypeCategory {
			http.Error(w, locale.Text("node.bad_parent"), http.StatusBadRequest)
			return
		}
		node, createErr := srv.Repos.Nodes.CreateForum(parent, title, slug, description, r.FormValue("allow_polls") == "1")
		err = createErr
		if err == nil && node != nil {
			node.Options.Prefixes = parsePrefixIDs(r.FormValue("prefixes"))
			err = srv.Store.PutJSON("node:"+strconv.FormatUint(node.ID, 10), node)
		}
		if err != nil {
			http.Error(w, locale.Text("node.forum_failed"), http.StatusInternalServerError)
			return
		}
	case "update":
		if !validNodeForm(title, slug, description) {
			http.Error(w, locale.Text("node.bad_fields"), http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
		if err != nil {
			http.Error(w, locale.Text("node.bad_id"), http.StatusBadRequest)
			return
		}
		node, ok := srv.Repos.Nodes.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		options := node.Options
		options.AllowPolls = node.Type == domain.TypeForum && r.FormValue("allow_polls") == "1"
		options.Prefixes = parsePrefixIDs(r.FormValue("prefixes"))
		parent := uint64(0)
		if node.Type == domain.TypeForum {
			parent, err = strconv.ParseUint(r.FormValue("parent_id"), 10, 64)
			parentNode, found := srv.Repos.Nodes.Get(parent)
			if err != nil || !found || parentNode.Type != domain.TypeCategory {
				http.Error(w, locale.Text("node.bad_parent"), http.StatusBadRequest)
				return
			}
		}
		if err := srv.Repos.Nodes.Update(id, title, slug, description, options); err != nil {
			http.Error(w, locale.Text("node.update_failed"), http.StatusInternalServerError)
			return
		}
		if node.ParentID != parent {
			if err := srv.Repos.Nodes.MoveNode(id, parent); err != nil {
				http.Error(w, locale.Text("node.move_failed"), http.StatusInternalServerError)
				return
			}
			srv.D.Perms.Invalidate()
		}
	case "delete-empty":
		id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
		if err != nil {
			http.Error(w, locale.Text("node.bad_id"), http.StatusBadRequest)
			return
		}
		if err := srv.Repos.Nodes.DeleteEmpty(id); err != nil {
			http.Error(w, locale.Text("node.not_empty"), http.StatusConflict)
			return
		}
	default:
		http.Error(w, locale.Text("node.bad_action"), http.StatusBadRequest)
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
	d := srv.base(r, srv.uiLocale().Text("admin.matrix"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
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
	d.AdminGroups = srv.availableGroups()
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
		for _, group := range srv.availableGroups() {
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
	for _, group := range srv.availableGroups() {
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
