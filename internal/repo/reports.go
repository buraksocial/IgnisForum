package repo

import (
	"fmt"
	"strings"
	"time"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/store"
)

const IdxOpenReports = "idx:reports_open"

func reportKey(id uint64) string { return fmt.Sprintf("report:%d", id) }
func reportUniqueKey(postID, reporterID uint64) string {
	return fmt.Sprintf("report_unique:%d:%d", postID, reporterID)
}

// CreateReport creates or reopens a member's report on a post. One active
// report per member/post bounds spam while preserving a moderation audit trail.
func (r *Repos) CreateReport(postID, reporterID uint64, reason string) (*domain.Report, error) {
	post, ok := r.Posts.Get(postID)
	if !ok || post.State != 0 {
		return nil, fmt.Errorf("post %d not found or unavailable", postID)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 500 {
		return nil, fmt.Errorf("report reason must be between 3 and 500 characters")
	}
	var existingID uint64
	if _, err := r.S.GetJSON(reportUniqueKey(postID, reporterID), &existingID); err == nil && existingID != 0 {
		var existing domain.Report
		if _, err := r.S.GetJSON(reportKey(existingID), &existing); err == nil {
			if existing.State == "open" {
				return &existing, nil
			}
			existing.State = "open"
			existing.Reason = reason
			existing.CreatedAt = time.Now().UTC()
			existing.ResolvedAt = time.Time{}
			existing.ResolvedBy = 0
			if err := r.S.PutJSON(reportKey(existing.ID), &existing); err != nil {
				return nil, err
			}
			r.S.ZAddIdx(IdxOpenReports, ts(existing.CreatedAt), existing.ID)
			return &existing, nil
		}
	}
	id := r.S.NextID()
	r.S.MarkID(store.TypeReport, id)
	report := &domain.Report{ID: id, PostID: post.ID, ThreadID: post.ThreadID,
		ReporterID: reporterID, Reason: reason, State: "open", CreatedAt: time.Now().UTC()}
	if err := r.S.PutJSON(reportKey(id), report); err != nil {
		return nil, err
	}
	if err := r.S.PutJSON(reportUniqueKey(postID, reporterID), id); err != nil {
		return nil, err
	}
	r.S.ZAddIdx(IdxOpenReports, ts(report.CreatedAt), report.ID)
	return report, nil
}

func (r *Repos) GetReport(id uint64) (*domain.Report, bool) {
	var report domain.Report
	if ok, _ := r.S.GetJSON(reportKey(id), &report); !ok {
		return nil, false
	}
	return &report, true
}

func (r *Repos) OpenReports(offset, limit int) []*domain.Report {
	ids := r.S.ZPage(IdxOpenReports, offset, limit)
	out := make([]*domain.Report, 0, len(ids))
	for _, id := range ids {
		if report, ok := r.GetReport(id); ok && report.State == "open" {
			out = append(out, report)
		}
	}
	return out
}

func (r *Repos) ResolveReport(id, adminID uint64) error {
	report, ok := r.GetReport(id)
	if !ok {
		return fmt.Errorf("report %d not found", id)
	}
	if report.State != "open" {
		return nil
	}
	report.State = "resolved"
	report.ResolvedBy = adminID
	report.ResolvedAt = time.Now().UTC()
	if err := r.S.PutJSON(reportKey(id), report); err != nil {
		return err
	}
	r.S.ZRem(IdxOpenReports, id)
	return nil
}

func (r *Repos) rebuildReportHook(_ string, blob []byte) {
	var report domain.Report
	if err := r.S.DecodeJSON(blob, &report); err != nil || report.ID == 0 {
		return
	}
	if report.State == "open" {
		r.S.ZAddIdx(IdxOpenReports, ts(report.CreatedAt), report.ID)
	}
}
