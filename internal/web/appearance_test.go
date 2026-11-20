package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buraksocial/IgnisForum/internal/perm"
)

func TestAppearanceAdministrationAndSidebar(t *testing.T) {
	ts, st, repos, authSvc, perms := newTestServer(t)
	admin := loginAsAdmin(t, ts, st, authSvc)
	member := register(t, ts, "widgetmember")
	path := ts.URL + "/admin/appearance"
	if code, _, _ := getBody(t, member, path); code != http.StatusForbidden {
		t.Fatalf("non-admin settings: %d", code)
	}
	if code, body, _ := getBody(t, admin, path+"?preview=ember"); code != 200 || !strings.Contains(body, `data-theme="ember"`) {
		t.Fatalf("theme preview: %d", code)
	}
	guest := noRedirectClient()
	if _, body, _ := getBody(t, guest, ts.URL+"/"); !strings.Contains(body, `data-theme="thred"`) {
		t.Fatal("preview changed guest theme")
	}
	csrf := mustFindCSRF(t, admin, path)
	form := url.Values{"csrf": {"bad"}, "default_theme": {"ember"}, "pinned": {"3"}, "statistics": {"1"}, "presence": {"1"}, "media": {"1"}, "ad_title": {"Sponsor"}, "ad_url": {"https://example.org/campaign"}}
	if resp := formPost(t, admin, path, form); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid CSRF: %d", resp.StatusCode)
	}
	form.Set("csrf", csrf)
	form.Set("ad_url", "javascript:alert(1)")
	if resp := formPost(t, admin, path, form); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid ad: %d", resp.StatusCode)
	}
	form.Set("ad_url", "https://example.org/campaign")
	form.Set("accent", "#4F83FF")
	if resp := formPost(t, admin, path, form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save settings: %d", resp.StatusCode)
	}
	if _, body, _ := getBody(t, member, ts.URL+"/"); !strings.Contains(body, "Aktif Üyeler") {
		t.Fatal("active widget missing for member")
	}
	_, body, _ := getBody(t, guest, ts.URL+"/")
	for _, want := range []string{`data-theme="ember"`, `style="--color-brand:#4F83FF"`, "Sabit Duyurular", "Forum İstatistikleri", "Çevrimiçi Üyeler", "Sponsor", `rel="sponsored nofollow noopener noreferrer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("guest sidebar missing %q", want)
		}
	}
	if strings.Contains(body, "widgetmember") || strings.Contains(body, "Aktif Üyeler") {
		t.Fatal("guest saw member presence identity")
	}
	if err := perms.SetGrant(perm.GroupGuest, perm.ScopeNode, 2, map[string]bool{perm.KeyForumView: false}); err != nil {
		t.Fatal(err)
	}
	_, body, _ = getBody(t, guest, ts.URL+"/")
	if strings.Contains(body, "Sabit Duyurular") || strings.Contains(body, "Demo konu") {
		t.Fatal("hidden forum announcement leaked")
	}
	if _, err := repos.Posts.Create(3, 1, "[img]https://example.org/photo.png[/img] [download]https://example.org/file.zip[/download]"); err != nil {
		t.Fatal(err)
	}
	_, body, _ = getBody(t, admin, ts.URL+"/t/3")
	if !strings.Contains(body, "Görseli önizle") || !strings.Contains(body, "Harici indirme bağlantısı") || strings.Contains(body, `<img src="https://example.org`) {
		t.Fatal("media controls missing or eagerly loaded remote image")
	}
	preview, err := guest.Get(ts.URL + "/media/preview?url=" + url.QueryEscape("https://example.org/photo.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	if preview.StatusCode != 200 || preview.Header.Get("Referrer-Policy") != "no-referrer" || preview.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("preview not isolated")
	}
	if code, _, _ := getBody(t, guest, ts.URL+"/media/preview?url="+url.QueryEscape("javascript:alert(1)")); code != 404 {
		t.Fatalf("unsafe preview: %d", code)
	}
}

func TestMediaMarkupAndPresenceLimits(t *testing.T) {
	items := postMedia("[code][img]https://example.org/a[/img][/code] [img]https://example.org/b[/link] [link]https://user@example.org/c[/link] [link]https://example.org/d[/link]")
	if len(items) != 1 || items[0].Kind != "link" || items[0].URL != "https://example.org/d" {
		t.Fatalf("media: %+v", items)
	}
	var p memberPresence
	now := time.Now()
	for uid := uint64(1); uid <= presenceLimit+1; uid++ {
		p.touch(uid, now)
	}
	if count, active := p.snapshot(now, true); count != presenceLimit || len(active) != 12 {
		t.Fatalf("presence count=%d shown=%d", count, len(active))
	}
	if count, active := p.snapshot(now.Add(6*time.Minute), true); count != presenceLimit || len(active) != 0 {
		t.Fatalf("active window count=%d shown=%d", count, len(active))
	}
	if count, _ := p.snapshot(now.Add(presenceWindow), false); count != 0 {
		t.Fatalf("expired presence count=%d", count)
	}
}
