package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buraksocial/IgnisForum/internal/plugin"
)

func TestAdminControlsInstalledPlugins(t *testing.T) {
	// Minimal no-import ABI v1 module, returning a static home.decorate notice.
	wasm, err := hex.DecodeString("0061736d010000000110036000017f60017f017f60027f7f017e0304030001020503010001073804066d656d6f727902001069676e69735f706c7567696e5f61706900000b69676e69735f616c6c6f6300010a69676e69735f686f6f6b00020a1103040041010b050041c0000b040042220b0b28010041000b227b226e6f7469636573223a5b22546573742077696467657420616374697665225d7d")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, pair := range [][2]string{{"base", ""}, {"child", "base"}, {"leaf", "child"}, {"other", ""}} {
		manifest := plugin.Manifest{ID: pair[0], Name: pair[0], Version: "1.0.0", APIVersion: 1, Hooks: []string{"home.decorate"}}
		if pair[1] != "" {
			manifest.Depends = []plugin.Dependency{{ID: pair[1], Version: "1.0.0"}}
		}
		dir := filepath.Join(root, pair[0])
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := plugin.Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	ts, st, _, authSvc, _ := newTestServer(t, m)
	admin := loginAsAdmin(t, ts, st, authSvc)
	member := register(t, ts, "pluginmember")
	panel := ts.URL + "/admin/appearance"
	csrf := mustFindCSRF(t, admin, panel)
	post := func(c *http.Client, id, action, token string, want int) {
		t.Helper()
		resp := formPost(t, c, panel+"/plugins/"+id, url.Values{"csrf": {token}, "action": {action}})
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d", action, id, resp.StatusCode, want)
		}
	}
	checkHome := func(want int) {
		t.Helper()
		code, body, _ := getBody(t, noRedirectClient(), ts.URL+"/")
		if got := strings.Count(body, "Test widget active"); code != 200 || got != want {
			t.Fatalf("home status=%d notices=%d want=%d", code, got, want)
		}
	}
	checkHome(4)
	post(member, "base", "disable", mustFindCSRF(t, member, ts.URL+"/"), http.StatusForbidden)
	post(admin, "base", "disable", "wrong", http.StatusForbidden)
	post(admin, "missing", "disable", csrf, http.StatusNotFound)
	post(admin, "base", "disable", csrf, http.StatusSeeOther)
	checkHome(1)
	_, body, _ := getBody(t, admin, panel)
	if !strings.Contains(body, "Bağımlılık kapalı: base") || !strings.Contains(body, "Bağımlılık kapalı: child") {
		t.Fatal("dependency status missing from panel")
	}
	post(admin, "leaf", "disable", csrf, http.StatusSeeOther)
	post(admin, "base", "enable", csrf, http.StatusSeeOther)
	checkHome(3)
	post(admin, "leaf", "enable", csrf, http.StatusSeeOther)
	checkHome(4)
}
