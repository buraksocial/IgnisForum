package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	assets "github.com/buraksocial/IgnisForum"
	"github.com/buraksocial/IgnisForum/internal/auth"
	"github.com/buraksocial/IgnisForum/internal/perm"
	"github.com/buraksocial/IgnisForum/internal/repo"
	"github.com/buraksocial/IgnisForum/internal/store"
)

// newTestServer builds the full M2 stack on a real engine (t.TempDir — mocks
// are forbidden): 1 category, 1 forum, 1 thread, 1 reply, plus the auth
// services and the stock permission matrix. It returns a live httptest.Server
// so the auth flows exercise real cookies.
func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *repo.Repos, *auth.Service, *perm.Engine) {
	t.Helper()
	s, err := store.Open(store.OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
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

	authSvc := auth.NewService(s)
	rl := auth.NewRateLimiter(s)
	users := auth.NewUserRepo(authSvc, rl)
	perms := perm.NewEngine(s)
	if err := perms.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Store: s, Repos: r, D: Deps{Auth: authSvc, Users: users, RL: rl, Perms: perms},
		css: []byte(".x{--color-brand:#ff4400}"), tmpl: loadTemplates(assets.FS)}
	ts := httptest.NewServer(srv.router())
	t.Cleanup(ts.Close)
	return ts, s, r, authSvc, perms
}

// router factors the M2 route wiring out of New for tests (identical chain).
func (srv *Server) router() http.Handler {
	rr := buildRouter(srv)
	return rr
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

// formPost posts x-www-form-urlencoded and returns the response (redirects
// followed=false via a client that never follows).
func formPost(t *testing.T, c *http.Client, url string, form url.Values) *http.Response {
	t.Helper()
	resp, err := c.PostForm(url, form)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// jarNoRedirect: keeps cookies (so the session sticks) but reports the first
// redirect response instead of following it — for asserting 303/302 flows.
func jarNoRedirect() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func getBody(t *testing.T, c *http.Client, url string) (int, string, http.Header) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b), resp.Header
}

// mustFindCSRF loads a page containing a csrf input and returns the token.
func mustFindCSRF(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	code, body, _ := getBody(t, c, url)
	if code != 200 {
		t.Fatalf("GET %s = %d", url, code)
	}
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in %s", url)
	}
	return m[1]
}

func preCSRF(t *testing.T, c *http.Client, pageURL string) string {
	t.Helper()
	return mustFindCSRF(t, c, pageURL)
}

// register creates a user via the real form and returns a client that is
// logged in (cookie carried by the client jar).
func register(t *testing.T, ts *httptest.Server, name string) *http.Client {
	t.Helper()
	c := jarNoRedirect() // keeps cookies, reports the first response
	form := url.Values{
		"username": {name},
		"email":    {name + "@example.com"},
		"password": {"geheim123"},
	}
	form.Set("csrf", preCSRF(t, c, ts.URL+"/register"))
	resp, err := c.PostForm(ts.URL+"/register", form)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("register = %d loc=%q (want 302 /)", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The redirect target must show the identity (session cookie works).
	code, body, _ := getBody(t, c, ts.URL+"/")
	if code != 200 || !strings.Contains(body, name) {
		t.Fatalf("logged-in home after register = %d (want 200 with %q)", code, name)
	}
	return c
}

func loginAsAdmin(t *testing.T, ts *httptest.Server, s *store.Store, authSvc *auth.Service) *http.Client {
	t.Helper()
	password := "test-admin-password-2026"
	hash, err := authSvc.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	users := auth.NewUserRepo(authSvc, auth.NewRateLimiter(s))
	if _, err := users.CreateUserDirect(50, "admin-test", "admin-test@example.com", hash, []uint64{perm.GroupAdmin}, 1); err != nil {
		t.Fatal(err)
	}
	c := jarNoRedirect()
	csrf := preCSRF(t, c, ts.URL+"/login")
	resp := formPost(t, c, ts.URL+"/login", url.Values{"username": {"admin-test"}, "password": {password}, "csrf": {csrf}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("admin login = %d", resp.StatusCode)
	}
	return c
}

func TestPagesSmoke(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)
	c := noRedirectClient()

	// Home: forum row + latest thread.
	code, body, _ := getBody(t, c, ts.URL+"/")
	if code != 200 {
		t.Fatalf("GET / = %d", code)
	}
	for _, want := range []string{"Sohbet", "Demo konu", "demo forum"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / missing %q", want)
		}
	}

	// Forum index + sort variants.
	code, body, _ = getBody(t, c, ts.URL+"/f/2")
	if code != 200 || !strings.Contains(body, "Demo konu") {
		t.Fatalf("GET /f/2 = %d", code)
	}
	for _, q := range []string{"?sort=new", "?sort=views", "?sort=posts"} {
		if code, _, _ := getBody(t, c, ts.URL+"/f/2"+q); code != 200 {
			t.Errorf("GET /f/2%s = %d", q, code)
		}
	}

	// Thread view: guest sees posts, NO reply box, login hint instead.
	code, body, _ = getBody(t, c, ts.URL+"/t/3")
	if code != 200 {
		t.Fatalf("GET /t/3 = %d", code)
	}
	for _, want := range []string{"Demo konu", "yanıt içeriği", "<strong>dünya</strong>", "giriş yapın"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /t/3 missing %q", want)
		}
	}
	if strings.Contains(body, "Hızlı Yanıt") {
		t.Error("guest must not see the reply editor")
	}

	// Guest POST reply → 302 to /login (auth gate, no session).
	resp, err := c.PostForm(ts.URL+"/t/3/reply", url.Values{"message": {"hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("guest POST reply = %d loc=%q, want 302 /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// Guest new-thread form → login redirect.
	if code, _, _ := getBody(t, c, ts.URL+"/f/2/new"); code != http.StatusFound {
		t.Errorf("GET /f/2/new guest = %d, want 302", code)
	}

	// Search works for guests (stock matrix: search allow).
	code, body, _ = getBody(t, c, ts.URL+"/search?q=aramakolay")
	if code != 200 || !strings.Contains(body, "Demo konu") {
		t.Errorf("GET /search = %d want hit", code)
	}

	// Latest, 404, healthz, css.
	if code, body, _ := getBody(t, c, ts.URL+"/latest"); code != 200 || !strings.Contains(body, "Demo konu") {
		t.Errorf("/latest = %d", code)
	}
	if code, _, _ := getBody(t, c, ts.URL+"/t/999"); code != http.StatusNotFound {
		t.Errorf("/t/999 = %d, want 404", code)
	}
	if code, body, _ := getBody(t, c, ts.URL+"/healthz"); code != 200 || !strings.Contains(body, "version") {
		t.Errorf("/healthz = %d", code)
	}
	if code, body, _ := getBody(t, c, ts.URL+"/s/thred/style.css"); code != 200 || !strings.Contains(body, "color-brand") {
		t.Errorf("style.css = %d", code)
	}
}

// TestAuthE2E is the M2 acceptance walk: register → auto-login → reply with
// CSRF → post visible under the new username → logout → writes blocked again.
func TestAuthE2E(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)

	c := register(t, ts, "zerap")

	// Header shows the user + logout.
	code, body, _ := getBody(t, c, ts.URL+"/")
	if code != 200 || !strings.Contains(body, "zerap") || !strings.Contains(body, "Çıkış") {
		t.Fatalf("logged-in home must show user + logout (%d)", code)
	}

	// Reply box visible now; grab CSRF from the rendered form.
	csrf := mustFindCSRF(t, c, ts.URL+"/t/3")

	resp, err := c.PostForm(ts.URL+"/t/3/reply", url.Values{
		"message": {"girişli yanıt [i]test[/i]"},
		"csrf":    {csrf},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST reply = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if !strings.Contains(loc, "#post-") {
		t.Errorf("reply Location = %q, want deep link", loc)
	}

	code, body, _ = getBody(t, c, ts.URL+"/t/3")
	if !strings.Contains(body, "girişli yanıt") || !strings.Contains(body, "zerap") {
		t.Errorf("reply/author not visible after POST (%d)", code)
	}

	// New thread via the form (member has forum_thread).
	csrf = mustFindCSRF(t, c, ts.URL+"/f/2/new")
	resp, err = c.PostForm(ts.URL+"/f/2/new", url.Values{
		"title": {"Üye konusu"}, "message": {"içerik"}, "csrf": {csrf},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST new thread = %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	// Logout (POST + CSRF) clears the session.
	csrf = mustFindCSRF(t, c, ts.URL+"/")
	resp, err = c.PostForm(ts.URL+"/logout", url.Values{"csrf": {csrf}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// After logout the editor is gone and writes bounce to login.
	if _, body, _ := getBody(t, c, ts.URL+"/t/3"); strings.Contains(body, "Hızlı Yanıt") {
		t.Error("reply editor visible after logout")
	}
	resp, err = c.PostForm(ts.URL+"/t/3/reply", url.Values{"message": {"ghost"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("post-logout reply = %d, want 302 login", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestCSRFRejection: authenticated POST with a bogus token → 403, and the
// message is not written.
func TestCSRFRejection(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)
	c := register(t, ts, "zed")

	if _, body, _ := getBody(t, c, ts.URL+"/t/3"); !strings.Contains(body, "Hızlı Yanıt") {
		t.Fatal("editor missing")
	}
	resp, err := c.PostForm(ts.URL+"/t/3/reply", url.Values{
		"message": {"csrf-dışı"}, "csrf": {"deadbeef"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bad csrf = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	if _, body, _ := getBody(t, c, ts.URL+"/t/3"); strings.Contains(body, "csrf-dışı") {
		t.Error("post written despite invalid CSRF")
	}
}

func TestReactionRequiresCSRFAndPersists(t *testing.T) {
	ts, _, r, authSvc, _ := newTestServer(t)
	c := register(t, ts, "reactor")
	var userID uint64
	baseURL, _ := url.Parse(ts.URL)
	for _, cookie := range c.Jar.Cookies(baseURL) {
		if cookie.Name == "ignis_sid" {
			if sess := authSvc.GetSession(cookie.Value); sess != nil {
				userID = sess.UserID
			}
		}
	}
	if userID == 0 {
		t.Fatal("registered session missing")
	}
	postID := r.Posts.List(3, 0, 1)[0]
	csrf := mustFindCSRF(t, c, ts.URL+"/t/3")
	resp := formPost(t, c, ts.URL+"/t/3/post/"+itoa(postID)+"/reaction", url.Values{"reaction": {"like"}, "csrf": {"bad"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad reaction CSRF = %d, want 403", resp.StatusCode)
	}
	if _, ok := r.Reactions.Get(postID, userID); ok {
		t.Fatal("reaction written with invalid CSRF")
	}
	resp = formPost(t, c, ts.URL+"/t/3/post/"+itoa(postID)+"/reaction", url.Values{"reaction": {"like"}, "csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reaction POST = %d, want 303", resp.StatusCode)
	}
	if kind, ok := r.Reactions.Get(postID, userID); !ok || kind != "like" {
		t.Fatalf("reaction = %q, %v", kind, ok)
	}
	_, body, _ := getBody(t, c, ts.URL+"/t/3")
	if !strings.Contains(body, "👍 Beğen <span>1</span>") {
		t.Fatal("reaction count not rendered")
	}
}

func TestAdminPermissionMatrixIsProtectedAndWritable(t *testing.T) {
	ts, s, _, authSvc, perms := newTestServer(t)
	member := register(t, ts, "ordinary")
	if code, _, _ := getBody(t, member, ts.URL+"/admin/permissions"); code != http.StatusForbidden {
		t.Fatalf("member admin access = %d, want 403", code)
	}
	admin := loginAsAdmin(t, ts, s, authSvc)
	code, body, _ := getBody(t, admin, ts.URL+"/admin/permissions?scope=*")
	if code != http.StatusOK || !strings.Contains(body, "İzin matrisi") {
		t.Fatalf("permission page = %d", code)
	}
	csrf := mustFindCSRF(t, admin, ts.URL+"/admin/permissions?scope=*")
	form := url.Values{"csrf": {csrf}, "scope": {perm.ScopeGlobal}}
	for _, groupID := range []uint64{perm.GroupGuest, perm.GroupMember, perm.GroupAdmin} {
		for _, key := range perm.AllKeys {
			form.Set("g"+itoa(groupID)+"_"+key, "inherit")
		}
	}
	form.Set("g"+itoa(perm.GroupMember)+"_"+perm.KeyForumReply, "deny")
	form.Set("g"+itoa(perm.GroupAdmin)+"_"+perm.KeyAdmin, "deny") // guarded against admin lockout
	resp := formPost(t, admin, ts.URL+"/admin/permissions", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("permission save = %d", resp.StatusCode)
	}
	if grants := perms.GetGrant(perm.GroupMember, perm.ScopeGlobal, 0); grants == nil || grants[perm.KeyForumReply] {
		t.Fatalf("member reply grant = %#v, expected explicit deny", grants)
	}
	if grants := perms.GetGrant(perm.GroupAdmin, perm.ScopeGlobal, 0); grants == nil || !grants[perm.KeyAdmin] || !grants[perm.KeyAccess] {
		t.Fatalf("admin safety grants = %#v", grants)
	}
}

// TestWrongCredentials covers the unified login failure + rate-limit lockout.
func TestWrongCredentials(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)

	c := register(t, ts, "alice")
	logoutCSRF := mustFindCSRF(t, c, ts.URL+"/")
	resp, err := c.PostForm(ts.URL+"/logout", url.Values{"csrf": {logoutCSRF}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Correct user, wrong password.
	login := jarNoRedirect()
	loginCSRF := preCSRF(t, login, ts.URL+"/login")
	resp, err = login.PostForm(ts.URL+"/login", url.Values{
		"username": {"alice"}, "password": {"yanlışşifre"}, "next": {"/t/3"}, "csrf": {loginCSRF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong pw = %d, want 401", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Unknown user: same status + message (no oracle).
	resp, err = login.PostForm(ts.URL+"/login", url.Values{
		"username": {"spuk"}, "password": {"yanlışşifre"}, "next": {"/t/3"}, "csrf": {loginCSRF},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown user = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(b), "Geçersiz kullanıcı adı veya şifre") {
		t.Error("login failure message not unified")
	}

	// Correct password logs in and honours next= (302 → /latest).
	loginNoRedirect := jarNoRedirect()
	loginCSRF = preCSRF(t, loginNoRedirect, ts.URL+"/login")
	resp, err = loginNoRedirect.PostForm(ts.URL+"/login", url.Values{
		"username": {"alice"}, "password": {"geheim123"}, "next": {"/latest"}, "csrf": {loginCSRF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/latest" {
		t.Errorf("login ok flow = %d loc=%q, want 302 /latest", resp.StatusCode, resp.Header.Get("Location"))
	}
	if len(resp.Header.Values("Set-Cookie")) == 0 ||
		!strings.Contains(strings.Join(resp.Header.Values("Set-Cookie"), ";"), "ignis_sid=") {
		t.Error("successful login did not set the session cookie")
	}
	resp.Body.Close()
}

// mustPath returns the final URL path after redirect tracking is disabled.
func mustPath(resp *http.Response) string {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.Path
	}
	return ""
}

// TestRateLimit: 6th reply inside 30s gets 429 + Retry-After.
func TestRateLimit(t *testing.T) {
	ts, _, _, _, _ := newTestServer(t)
	c := register(t, ts, "flood")
	csrf := mustFindCSRF(t, c, ts.URL+"/t/3")

	var last *http.Response
	for i := 0; i < 6; i++ {
		resp, err := c.PostForm(ts.URL+"/t/3/reply", url.Values{
			"message": {"flood"}, "csrf": {csrf},
		})
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		last = resp
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th reply = %d, want 429", last.StatusCode)
	}
	if ra := last.Header.Get("Retry-After"); ra == "" {
		t.Error("429 without Retry-After")
	}
}

// TestSessionSurvivesRebuild: sessions are KV primaries (§3), not RAM
// indexes — a full RebuildContent (which wipes + re-derives every ZSET/FT)
// must leave the logged-in session readable.
func TestSessionSurvivesRebuild(t *testing.T) {
	ts, s, r, authSvc, _ := newTestServer(t)
	c := register(t, ts, "keepme")

	var sid string
	for _, ck := range c.Jar.Cookies(tsURL(t, ts.URL)) {
		if ck.Name == "ignis_sid" {
			sid = ck.Value
		}
	}
	if sid == "" {
		t.Fatal("no ignis_sid cookie after register")
	}
	if sess := authSvc.GetSession(sid); sess == nil {
		t.Fatal("session missing pre-rebuild")
	}

	rep := s.RebuildContent(r.Hooks)
	if rep.Recovered == 0 {
		t.Fatal("rebuild recovered nothing")
	}
	sess := authSvc.GetSession(sid)
	if sess == nil || sess.UserID == 0 {
		t.Fatal("session lost across rebuild — sessions must live in the KV primary")
	}

	// And the user still browses logged-in after the rebuild.
	if _, body, _ := getBody(t, c, ts.URL+"/"); !strings.Contains(body, "keepme") {
		t.Error("identity lost after rebuild")
	}
}

func tsURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
