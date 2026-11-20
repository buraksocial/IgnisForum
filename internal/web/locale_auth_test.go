package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestEnglishAuthValidationAndMemberScreens(t *testing.T) {
	ts, _, _, _, _ := newTestServerWithDeps(t, Deps{PreviewLocale: "en"})
	c := jarNoRedirect()
	for path, want := range map[string]string{"/login": "Username or email", "/register": "Create account", "/f/2": "Date started"} {
		code, body, _ := getBody(t, c, ts.URL+path)
		if code != 200 || !strings.Contains(body, want) || !strings.Contains(body, `lang="en"`) {
			t.Fatalf("%s not localized: %d", path, code)
		}
	}
	post := func(path string, form url.Values, wantCode int, want string) {
		t.Helper()
		resp, err := c.PostForm(ts.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != wantCode || !strings.Contains(string(data), want) {
			t.Fatalf("%s status=%d, want %d and %q; body=%s", path, resp.StatusCode, wantCode, want, data)
		}
	}
	post("/login", url.Values{"csrf": {"wrong"}}, 403, "The form could not be verified.")
	csrf := mustFindCSRF(t, c, ts.URL+"/login")
	post("/login", url.Values{"csrf": {csrf}, "username": {"unknown"}, "password": {"wrongpassword"}}, 401, "Invalid username or password.")
	for _, tc := range []struct{ username, email, password, want string }{
		{"x", "one@example.org", "password123", "Invalid username."},
		{"validname", "invalid", "password123", "Invalid email address."},
		{"validname", "one@example.org", "short", "Use a password of at least 8 characters."},
	} {
		csrf = mustFindCSRF(t, c, ts.URL+"/register")
		post("/register", url.Values{"csrf": {csrf}, "username": {tc.username}, "email": {tc.email}, "password": {tc.password}}, 400, tc.want)
	}
	profileServer, _, _, _, _ := newTestServerWithDeps(t, Deps{PreviewLocale: "en"})
	member := register(t, profileServer, "englishmember")
	_, home, _ := getBody(t, member, profileServer.URL+"/")
	// The header profile link binds the generated ID without depending on seed IDs.
	start := strings.Index(home, `href="/members/`)
	if start < 0 {
		t.Fatal("profile link missing")
	}
	link := strings.SplitN(home[start+len(`href="`):], `"`, 2)[0]
	code, body, _ := getBody(t, member, profileServer.URL+link)
	if code != 200 || !strings.Contains(body, "Edit profile") || !strings.Contains(body, "Member title") {
		t.Fatalf("profile not localized: %d", code)
	}
	resp, err := member.PostForm(profileServer.URL+link+"/profile", url.Values{"csrf": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(data), "The form could not be verified.") {
		t.Fatalf("profile error not localized: %d", resp.StatusCode)
	}
}
