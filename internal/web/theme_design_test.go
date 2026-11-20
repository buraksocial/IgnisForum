package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestThemeDraftSaveAndRestore(t *testing.T) {
	for _, tag := range []string{"", "en"} {
		t.Run("locale="+tag, func(t *testing.T) { testThemeDraftSaveAndRestore(t, tag) })
	}
}

func testThemeDraftSaveAndRestore(t *testing.T, tag string) {
	ts, st, _, authSvc, _ := newTestServerWithDeps(t, Deps{PreviewLocale: tag})
	locale := turkish
	if tag == "en" {
		locale = englishPreview
	}
	if err := st.PutRaw(appearanceKey, []byte(`{"DefaultTheme":"thred","Accent":"#101010","Statistics":true}`)); err != nil {
		t.Fatal(err)
	}
	admin := loginAsAdmin(t, ts, st, authSvc)
	member := register(t, ts, "themeeditor")
	guest := noRedirectClient()
	path := ts.URL + "/admin/appearance"
	csrf := mustFindCSRF(t, admin, path)
	form := url.Values{
		"csrf": {csrf}, "intent": {"preview"}, "default_theme": {"ember"},
		"accent": {"#ffeeaa"}, "theme_font": {"serif"}, "site_width": {"1440"}, "corner_radius": {"0"},
		"ad_title": {"Sponsor"}, "ad_url": {"https://example.org/"}, "pinned": {"3"}, "media": {"1"},
	}
	for _, c := range []*http.Client{member, admin} {
		form.Set("csrf", "invalid")
		if resp := formPost(t, c, path, form); resp.StatusCode != 403 {
			t.Fatalf("unauthorized preview: %d", resp.StatusCode)
		}
	}
	form.Set("csrf", csrf)
	resp, err := admin.PostForm(path, form)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("draft status %d, read error %v", resp.StatusCode, err)
	}
	for _, want := range []string{locale.Text("appearance.draft"), locale.Text("appearance.save"), locale.Text("appearance.wasm"), locale.Text("appearance.attachments"), `data-theme="ember"`, "--color-brand:#FFEEAA", "--color-brand-contrast:#000000", "--font-body:Georgia,serif", "--max-width-site:1440px", "--radius-theme:0px", "</html>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	_, body, _ := getBody(t, guest, ts.URL+"/")
	if !strings.Contains(body, `data-theme="thred"`) || !strings.Contains(body, "--color-brand:#101010") || strings.Contains(body, "1440px") {
		t.Fatal("draft changed live appearance or old settings did not load")
	}
	form.Set("intent", "save")
	for field, bad := range map[string]string{"accent": "#fff; color:red", "theme_font": "url('http://example.org')", "site_width": "1801", "corner_radius": "-1"} {
		orig := form.Get(field)
		form.Set(field, bad)
		if resp := formPost(t, admin, path, form); resp.StatusCode != 400 {
			t.Fatalf("invalid %s accepted: %d", field, resp.StatusCode)
		}
		form.Set(field, orig)
	}
	if resp := formPost(t, admin, path, form); resp.StatusCode != 303 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	_, body, _ = getBody(t, guest, ts.URL+"/")
	if !strings.Contains(body, `data-theme="ember"`) || !strings.Contains(body, "--max-width-site:1440px") {
		t.Fatal("theme was not activated")
	}
	form.Set("ad_title", "New sponsor")
	if resp := formPost(t, admin, path, form); resp.StatusCode != 303 {
		t.Fatalf("widget-only save: %d", resp.StatusCode)
	}
	var before appearanceSettings
	if _, err := st.GetJSON(appearanceKey, &before); err != nil || before.PreviousTheme == nil || before.PreviousTheme.DefaultTheme != "thred" {
		t.Fatalf("saved history not retained: %+v, %v", before.PreviousTheme, err)
	}
	restore := url.Values{"csrf": {"bad"}, "intent": {"restore"}}
	if resp := formPost(t, admin, path, restore); resp.StatusCode != 403 {
		t.Fatalf("restore without CSRF: %d", resp.StatusCode)
	}
	restore.Set("csrf", csrf)
	if resp := formPost(t, admin, path, restore); resp.StatusCode != 303 {
		t.Fatalf("restore: %d", resp.StatusCode)
	}
	_, body, _ = getBody(t, guest, ts.URL+"/")
	for _, want := range []string{`data-theme="thred"`, "--color-brand:#101010", "--color-brand-contrast:#FFFFFF", "New sponsor", locale.Text("home.pinned")} {
		if !strings.Contains(body, want) {
			t.Errorf("restored page lacks %q", want)
		}
	}
	if strings.Contains(body, "1440px") || strings.Contains(body, locale.Text("home.stats")) {
		t.Fatal("theme restore left the draft width or reverted widget settings")
	}
	if resp := formPost(t, admin, path, restore); resp.StatusCode != 409 {
		t.Fatalf("restore with no previous settings: %d", resp.StatusCode)
	}
}

func TestThemeCSSRevalidatesStoredValues(t *testing.T) {
	d := themeDesign{Accent: "#fff;background:red", Font: "unknown", SiteWidth: "9999", Radius: "1px;"}
	if got := d.CSS(); got != "" {
		t.Fatalf("invalid values reached CSS: %q", got)
	}
}
