package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

const adminAuditPageSize = 50

func adminAuditKey(id uint64) string { return fmt.Sprintf("admin_audit:%d", id) }

// recordAdminAudit writes the primary audit event after a successful mutation.
// Audit failures are returned to callers so they can surface an operational
// error instead of silently claiming that a privileged action was recorded.
func (srv *Server) recordAdminAudit(actorID uint64, action, targetType string, targetID uint64, details string) error {
	id := srv.Store.NextID()
	srv.Store.MarkID(store.TypeAdminAudit, id)
	event := &domain.AdminAudit{ID: id, ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID, Details: details, CreatedAt: time.Now().UTC()}
	return srv.Store.PutJSON(adminAuditKey(id), event)
}

func (srv *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	page := parseInt(r, "p", 1)
	if page < 1 {
		page = 1
	}
	max := srv.Store.MaxID(store.TypeAdminAudit)
	total := 0
	start := (page - 1) * adminAuditPageSize
	skipped := 0
	d := srv.base(r, srv.uiLocale().Text("admin.audit"), "admin", []Crumb{{URL: "/admin", Title: srv.uiLocale().Text("nav.admin")}})
	for id := max; id > 0; id-- {
		var event domain.AdminAudit
		ok, err := srv.Store.GetJSON(adminAuditKey(id), &event)
		if err != nil || !ok {
			continue
		}
		total++
		if skipped < start {
			skipped++
			continue
		}
		if len(d.AdminAudit) >= adminAuditPageSize {
			continue
		}
		d.AdminAudit = append(d.AdminAudit, adminAuditRow{ID: event.ID, Actor: srv.username(event.ActorID), Action: event.Action,
			TargetType: event.TargetType, TargetID: event.TargetID, Details: event.Details, CreatedAt: event.CreatedAt})
	}
	d.Page, d.TotalPages, d.PageWindow = page, pageCount(total, adminAuditPageSize), pageWindow(page, pageCount(total, adminAuditPageSize))
	if d.TotalPages > 0 && page > d.TotalPages {
		d.Page = d.TotalPages
	}
	srv.render(w, "admin_audit", d)
}
