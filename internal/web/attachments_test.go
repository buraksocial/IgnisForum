package web

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/buraksocial/IgnisForum/internal/perm"
)

func TestLocalAttachmentLifecycleAndVisibility(t *testing.T) {
	ts, st, repos, authSvc, perms := newTestServer(t)
	admin := loginAsAdmin(t, ts, st, authSvc)
	member := register(t, ts, "attachmentmember")
	guest := noRedirectClient()
	postID := repos.Posts.List(3, 1, 1)[0]
	threadURL := ts.URL + "/t/3"
	postURL := threadURL + "/post/" + itoa(postID)
	attachmentURL := postURL + "/attachment"
	adminURL := ts.URL + "/admin/appearance"
	csrf := mustFindCSRF(t, admin, adminURL)
	memberCSRF := mustFindCSRF(t, member, threadURL)
	const filename = "örnek.html"
	const content = "<h1>Saved as a download</h1>"
	check := func(c *http.Client, path string, want int) string {
		t.Helper()
		code, body, _ := getBody(t, c, path)
		if code != want {
			t.Fatalf("%s: got %d, want %d", path, code, want)
		}
		return body
	}
	check(guest, attachmentURL, http.StatusNotFound)
	if got := postAttachment(t, admin, attachmentURL, csrf, filename, []byte(content)); got != http.StatusNotFound {
		t.Fatalf("upload before enable: %d", got)
	}
	if resp := formPost(t, admin, adminURL, url.Values{"csrf": {csrf}, "default_theme": {"thred"}, "attachments": {"1"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("enable attachments: %d", resp.StatusCode)
	}
	if !strings.Contains(check(admin, threadURL, 200), `enctype="multipart/form-data"`) {
		t.Fatal("upload form missing")
	}
	if got := postAttachment(t, member, attachmentURL, memberCSRF, filename, []byte(content)); got != http.StatusForbidden {
		t.Fatalf("non-owner upload: %d", got)
	}
	if got := postAttachment(t, admin, attachmentURL, "wrong", filename, []byte(content)); got != http.StatusForbidden {
		t.Fatalf("upload with invalid CSRF: %d", got)
	}
	if got := postAttachment(t, admin, attachmentURL, csrf, filename, make([]byte, attachmentLimit+1)); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d", got)
	}
	if got := postAttachment(t, admin, attachmentURL, csrf, filename, []byte(content)); got != http.StatusSeeOther {
		t.Fatalf("valid upload: %d", got)
	}
	if got := postAttachment(t, admin, attachmentURL, csrf, filename, []byte(content)); got != http.StatusConflict {
		t.Fatalf("duplicate upload: %d", got)
	}
	if !strings.Contains(check(guest, threadURL, 200), filename) {
		t.Fatal("download link missing")
	}
	resp, err := guest.Get(attachmentURL)
	if err != nil {
		t.Fatal(err)
	}
	b, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	disposition, params, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if resp.StatusCode != 200 || readErr != nil || string(b) != content || parseErr != nil || disposition != "attachment" || params["filename"] != filename || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download status=%d headers=%v read error=%v", resp.StatusCode, resp.Header, readErr)
	}
	check(guest, ts.URL+"/t/999/post/"+itoa(postID)+"/attachment", 404)
	if err := perms.SetGrant(perm.GroupGuest, perm.ScopeNode, 2, map[string]bool{perm.KeyForumView: false}); err != nil {
		t.Fatal(err)
	}
	check(guest, attachmentURL, 404)
	check(member, attachmentURL, 200)
	if resp := formPost(t, member, attachmentURL+"/remove", url.Values{"csrf": {memberCSRF}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner removal: %d", resp.StatusCode)
	}
	if resp := formPost(t, admin, attachmentURL+"/remove", url.Values{"csrf": {csrf}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove attachment: %d", resp.StatusCode)
	}
	check(member, attachmentURL, 404)
	if _, found := st.GetRaw(attachmentKey(postID)); found {
		t.Fatal("blob still present after removal")
	}
	if got := postAttachment(t, admin, attachmentURL, csrf, filename, []byte(content)); got != http.StatusSeeOther {
		t.Fatalf("re-upload: %d", got)
	}
	if resp := formPost(t, admin, postURL+"/delete", url.Values{"csrf": {csrf}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete post: %d", resp.StatusCode)
	}
	check(admin, attachmentURL, 404)
	if _, found := st.GetRaw(attachmentKey(postID)); found {
		t.Fatal("deleted post left attachment bytes")
	}
}

func postAttachment(t *testing.T, client *http.Client, path, csrf, filename string, payload []byte) int {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("csrf", csrf); err != nil {
		t.Fatal(err)
	}
	f, err := w.CreateFormFile("attachment", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(path, w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
