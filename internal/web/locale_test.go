package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	assets "github.com/buraksocial/IgnisForum"
)

// Check literal server-side lookups as well as embedded templates. This catches
// typos that would otherwise silently fall back to displaying a catalog key.
func TestServerLocaleKeys(t *testing.T) {
	files, err := fs.Glob(os.DirFS("."), "*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Text" && sel.Sel.Name != "Count") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			key, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if sel.Sel.Name == "Count" {
				key += ".other"
			}
			for _, locale := range []*uiLocale{turkish, englishPreview} {
				if locale.messages[key] == "" {
					t.Errorf("%s: %s lacks %q", file, locale.Tag, key)
				}
			}
			return true
		})
	}
}

func TestLocalizedTemplateKeys(t *testing.T) {
	keys := regexp.MustCompile(`\.Locale\.(Text|Count) "([a-z_.]+)"`)
	files, err := fs.Glob(assets.FS, "templates/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := assets.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, parts := range keys.FindAllStringSubmatch(string(data), -1) {
			key := parts[2]
			if parts[1] == "Count" {
				key += ".other"
			}
			if turkish.messages[key] == "" {
				t.Errorf("%s: Turkish catalog lacks %q", file, key)
			}
		}
	}
	for key := range turkish.messages {
		if englishPreview.messages[key] == "" {
			t.Errorf("English preview lacks %q", key)
		}
	}
}

func TestLocalizedCountsAndDates(t *testing.T) {
	now := time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		locale *uiLocale
		delta  time.Duration
		want   string
	}{
		{englishPreview, time.Second, "just now"},
		{englishPreview, time.Minute, "1 minute ago"},
		{englishPreview, 2 * time.Minute, "2 minutes ago"},
		{englishPreview, time.Hour, "1 hour ago"},
		{englishPreview, 24 * time.Hour, "1 day ago"},
		{englishPreview, 30 * 24 * time.Hour, "21 Oct 2026"},
		{turkish, 30 * 24 * time.Hour, "21.10.2026"},
	} {
		if got := tc.locale.Ago(now.Add(-tc.delta), now); got != tc.want {
			t.Errorf("%s %v: got %q, want %q", tc.locale.Tag, tc.delta, got, tc.want)
		}
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "0 threads"}, {1, "1 thread"}, {2, "2 threads"}} {
		if got := englishPreview.Count("count.threads", tc.n); got != tc.want {
			t.Errorf("count %d: got %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestLocaleHTTPPreview(t *testing.T) {
	prod, _, _, _, _ := newTestServer(t)
	guest := noRedirectClient()
	code, body, _ := getBody(t, guest, prod.URL+"/?locale=en")
	if code != http.StatusOK || !strings.Contains(body, `lang="tr" dir="ltr"`) || !strings.Contains(body, "Forum İstatistikleri") {
		t.Fatalf("production default changed: status %d", code)
	}

	preview, st, repos, _, _ := newTestServerWithDeps(t, Deps{PreviewLocale: "en"})
	if err := st.PutJSON(appearanceKey, appearanceSettings{themeDesign: themeDesign{DefaultTheme: "thred"}, Statistics: true, Media: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Posts.Create(3, 1, "[img]https://example.org/photo.png[/img]"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
		want []string
	}{
		{"/", 200, []string{"Forum statistics", "1 thread · 3 posts", "Demo konu"}},
		{"/latest", 200, []string{"<h1>Latest activity</h1>", "Demo konu"}},
		{"/search?q=" + url.QueryEscape("<test>"), 200, []string{"No results for “&lt;test&gt;”."}},
		{"/t/3", 200, []string{"Preview image"}},
		{"/media/preview?url=" + url.QueryEscape("https://example.org/photo.png"), 200, []string{"Image preview", "External image"}},
		{"/missing-page", 404, []string{"404 — Not found"}},
	} {
		code, body, _ := getBody(t, guest, preview.URL+tc.path)
		if code != tc.code || !strings.Contains(body, `lang="en" dir="ltr"`) {
			t.Errorf("%s: status %d; response %s", tc.path, code, body)
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", tc.path, want)
			}
		}
	}
	member := register(t, preview, "localemember")
	if code, body, _ := getBody(t, member, preview.URL+"/notifications"); code != 200 || !strings.Contains(body, "You have no notifications yet.") {
		t.Errorf("notifications not localized, status=%d", code)
	}
}
