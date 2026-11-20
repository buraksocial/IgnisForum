package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestEnglishThreadAndPollScreens(t *testing.T) {
	ts, st, repos, authSvc, _ := newTestServerWithDeps(t, Deps{PreviewLocale: "en"})
	admin := loginAsAdmin(t, ts, st, authSvc)
	forum, _ := repos.Nodes.Get(2)
	forum.Options.AllowPolls = true
	if err := st.PutJSON("node:2", forum); err != nil {
		t.Fatal(err)
	}
	if err := repos.Polls.Create(3, "Seçiminiz nedir?", []string{"Bir", "İki"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Posts.Edit(repos.Posts.List(3, 1, 1)[0], "Düzenlenmiş mesaj"); err != nil {
		t.Fatal(err)
	}
	check := func(client *http.Client, path string, wants ...string) {
		t.Helper()
		code, body, _ := getBody(t, client, ts.URL+path)
		if code != http.StatusOK {
			t.Fatalf("%s status %d", path, code)
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		for _, forbidden := range []string{"Foruma dön", "Konuyu takip et", "Oy ver", "Mesaj tepkileri", "Hızlı Yanıt", "Konuyu Aç"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s has unlocalized copy %q", path, forbidden)
			}
		}
	}
	check(noRedirectClient(), "/t/3", "Back to forum", "Log in to vote.", "Log in to reply.", "Demo konu", "Düzenlenmiş mesaj", "Last edited:")
	check(admin, "/t/3", "Follow thread", "Quick reply", "Save changes", "Delete this reply", "Send to moderators", "👍 Like", "❤️ Love", "Seçiminiz nedir?", "Bir", "İki")
	check(admin, "/f/2/new", "New thread — Sohbet", "Poll (optional)", "Allow multiple choices", "Create thread")
	csrf := mustFindCSRF(t, admin, ts.URL+"/t/3")
	if resp := formPost(t, admin, ts.URL+"/t/3/poll/vote", url.Values{"csrf": {csrf}, "option": {"0"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("vote: %d", resp.StatusCode)
	}
	check(admin, "/t/3", "Your vote has been recorded.", "Your choice")
}
