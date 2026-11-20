package web

import (
	"strings"
	"testing"

	"github.com/buraksocial/IgnisForum/internal/perm"
)

func TestEnglishAdministrationAndPermissionLabels(t *testing.T) {
	ts, st, repos, authSvc, _ := newTestServerWithDeps(t, Deps{PreviewLocale: "en"})
	admin := loginAsAdmin(t, ts, st, authSvc)
	for path, wants := range map[string][]string{
		"/admin":                               {"Administration center", "Manage themes and plugins", "Report queue", "Security"},
		"/admin/reports":                       {"Moderation reports", "There are no open reports."},
		"/admin/permissions":                   {"Permission matrix", "Administrator", "View forums", "Inherit", "Allow", "Deny", `value="inherit"`, `value="allow"`, `value="deny"`, perm.KeyForumView},
		"/admin/permissions?scope=node&node=2": {"Permission matrix", "Category", "Sohbet"},
	} {
		code, body, _ := getBody(t, admin, ts.URL+path)
		if code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
	if _, err := repos.CreateReport(repos.Posts.List(3, 1, 1)[0], 50, "Türkçe rapor gerekçesi"); err != nil {
		t.Fatal(err)
	}
	code, body, _ := getBody(t, admin, ts.URL+"/admin/reports")
	if code != 200 || !strings.Contains(body, "Go to post") || !strings.Contains(body, "Resolve") || !strings.Contains(body, "Türkçe rapor gerekçesi") {
		t.Fatal("report labels or user content missing")
	}
	for _, group := range adminGroups {
		if englishPreview.messages[group.Name] == "" || turkish.messages[group.Name] == "" {
			t.Errorf("group label missing: %q", group.Name)
		}
	}
	for _, key := range permissionLabels {
		if englishPreview.messages[key] == "" || turkish.messages[key] == "" {
			t.Errorf("permission label missing: %q", key)
		}
	}
}
