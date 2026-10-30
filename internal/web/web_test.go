package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	assets "github.com/buraksocial/IgnisForum"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// newTestServer builds the full M1 stack on a real engine (t.TempDir) with a
// minimal seeded tree: 1 category, 1 forum, 1 thread, 1 reply.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(store.OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := repo.New(s)
	cat, err := r.Nodes.CreateCategory("Genel", "genel", "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Nodes.CreateForum(cat.ID, "Sohbet", "sohbet", "demo forum", false)
	if err != nil {
		t.Fatal(err)
	}
	th, _, err := r.Threads.Create(f.ID, 1, "Demo konu", "merhaba [b]dünya[/b] aramakolay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Posts.Create(th.ID, 2, "yanıt içeriği"); err != nil {
		t.Fatal(err)
	}
	t.Log("ids: cat=", cat.ID, "forum=", f.ID, "thread=", th.ID)
	return New(s, r, assets.FS)
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var sb strings.Builder
	first := true
	for k, v := range form {
		if !first {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
		first = false
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(sb.String()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPagesSmoke(t *testing.T) {
	h := newTestServer(t)

	// Home: forum row + latest thread.
	rec := get(t, h, "/")
	if rec.Code != 200 {
		t.Fatalf("GET / = %d: %s", rec.Code, rec.Body.String()[:min(400, rec.Body.Len())])
	}
	for _, want := range []string{"Sohbet", "Demo konu", "demo forum"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET / missing %q", want)
		}
	}

	// Forum index.
	rec = get(t, h, "/f/2")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Demo konu") {
		t.Fatalf("GET /f/2 = %d (want 200 + Demo konu)", rec.Code)
	}
	// Sort variants respond.
	for _, q := range []string{"?sort=new", "?sort=views", "?sort=posts"} {
		if rec := get(t, h, "/f/2"+q); rec.Code != 200 {
			t.Errorf("GET /f/2%s = %d", q, rec.Code)
		}
	}

	// Thread view: both posts, rendered BBCode, reply box present.
	rec = get(t, h, "/t/3")
	if rec.Code != 200 {
		t.Fatalf("GET /t/3 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Demo konu", "yanıt içeriği", "<strong>dünya</strong>", "Hızlı Yanıt"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /t/3 missing %q", want)
		}
	}

	// Reply via POST → 303 redirect, then the post is visible.
	rec = post(t, h, "/t/3/reply", map[string]string{"message": "yeni yanıt [i]test[/i]"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST reply = %d, want 303: %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	rec = get(t, h, "/t/3")
	if !strings.Contains(rec.Body.String(), "yeni yanıt") {
		t.Error("reply not visible after POST")
	}

	// New thread via POST → 303 to the new thread.
	rec = post(t, h, "/f/2/new", map[string]string{"title": "Yeni başlık", "message": "içerik"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST new thread = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/t/") {
		t.Fatalf("POST new thread Location = %q, want /t/...", loc)
	}
	rec = get(t, h, loc)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Yeni başlık") {
		t.Errorf("GET %s = %d (want 200 + Yeni başlık)", loc, rec.Code)
	}

	// Search: term present in a post body finds the thread.
	rec = get(t, h, "/search?q=aramakolay")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Demo konu") {
		t.Errorf("GET /search?q=aramakolay = %d, want hit", rec.Code)
	}
	// No results for a nonsense term.
	rec = get(t, h, "/search?q=zzzqzqq")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "sonuç bulunamadı") {
		t.Errorf("GET /search empty case = %d", rec.Code)
	}

	// Latest page.
	if rec := get(t, h, "/latest"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Demo konu") {
		t.Errorf("GET /latest = %d", rec.Code)
	}

	// 404.
	if rec := get(t, h, "/t/999"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /t/999 = %d, want 404", rec.Code)
	}

	// healthz JSON.
	rec = get(t, h, "/healthz")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "version") {
		t.Errorf("GET /healthz = %d: %s", rec.Code, rec.Body.String())
	}

	// Theme css is served.
	if rec := get(t, h, "/s/thred/style.css"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "color-brand") {
		t.Errorf("GET /s/thred/style.css = %d", rec.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
