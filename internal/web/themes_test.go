package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	assets "github.com/buraksocial/IgnisForum"
)

func TestEmbeddedThemeInheritance(t *testing.T) {
	m, err := LoadThemeManager(assets.FS, "")
	if err != nil {
		t.Fatal(err)
	}
	thred, base, ok := m.Get("thred")
	if !ok || thred.Name != "Thred" {
		t.Fatalf("default theme = %+v, %v", thred, ok)
	}
	ember, css, ok := m.Get("ember")
	if !ok || ember.Name != "Ember" {
		t.Fatalf("child theme = %+v, %v", ember, ok)
	}
	if ember.Version == thred.Version || !strings.Contains(string(css), "--color-brand: #ff4400") || !strings.Contains(string(css), "--color-brand: #bd542f") {
		t.Fatal("parent and child styles were not composed in order")
	}
	if len(base) == 0 {
		t.Fatal("base theme CSS is empty")
	}
}

func TestExternalThemeValidation(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "custom")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "theme.json"), []byte(`{"id":"custom","name":"Custom","version":"1","parent":"thred"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "style.css"), []byte(":root { --color-brand: #123456; }"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadThemeManager(assets.FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, css, ok := m.Get("custom"); !ok || !strings.Contains(string(css), "#123456") {
		t.Fatal("external theme was not loaded")
	}
	if err := os.Remove(filepath.Join(child, "theme.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "theme.json"), []byte(`{"id":"custom","name":"Custom","version":"1","parent":"missing"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadThemeManager(assets.FS, dir); err == nil {
		t.Fatal("missing theme parent accepted")
	}
}

func TestGuestThemeSelection(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)
	c := jarNoRedirect()
	resp, err := c.Get(ts.URL + "/theme/ember?next=" + url.QueryEscape("//attacker.example"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("theme redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, body, _ := getBody(t, c, ts.URL+"/")
	if code != http.StatusOK || !strings.Contains(body, `data-theme="ember"`) || !strings.Contains(body, "/s/ember/style.css?v=") {
		t.Fatalf("selected theme not rendered (%d)", code)
	}
	code, css, _ := getBody(t, c, ts.URL+"/s/ember/style.css")
	if code != http.StatusOK || !strings.Contains(css, "--color-brand: #bd542f") {
		t.Fatal("theme CSS route failed")
	}
}

func TestMemberThemePreferenceIsPersistent(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)
	c := register(t, ts, "themer")
	resp, err := c.Get(ts.URL + "/theme/ember?next=/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("theme select = %d", resp.StatusCode)
	}
	code, body, _ := getBody(t, c, ts.URL+"/")
	if code != http.StatusOK || !strings.Contains(body, `data-theme="ember"`) {
		t.Fatal("member theme preference was not persisted")
	}
}
