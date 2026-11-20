package web

import (
	"io"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestAdminNodeLocaleAndParentSelection(t *testing.T) {
	for _, tag := range []string{"", "en"} {
		t.Run("locale="+tag, func(t *testing.T) {
			ts, st, repos, authSvc, _ := newTestServerWithDeps(t, Deps{PreviewLocale: tag})
			admin := loginAsAdmin(t, ts, st, authSvc)
			cat, err := repos.Nodes.CreateCategory("İkinci kategori", "second", "")
			if err != nil {
				t.Fatal(err)
			}
			f, err := repos.Nodes.CreateForum(cat.ID, "İkinci forum", "second-forum", "", true)
			if err != nil {
				t.Fatal(err)
			}
			path := ts.URL + "/admin/nodes"
			code, body, _ := getBody(t, admin, path)
			if code != 200 || !strings.Contains(body, "</html>") || strings.Contains(body, "template:") {
				t.Fatal("node screen did not render completely")
			}
			forms := regexp.MustCompile(`(?s)<form class="panel admin-node-edit".*?</form>`).FindAllString(body, -1)
			for id, parent := range map[uint64]uint64{2: 1, f.ID: cat.ID} {
				found := false
				for _, form := range forms {
					if !strings.Contains(form, `name="id" value="`+itoa(id)+`"`) {
						continue
					}
					found = true
					if !strings.Contains(form, `<option value="`+itoa(parent)+`" selected>`) || strings.Count(form, " selected>") != 1 {
						t.Errorf("forum %d has wrong parent selection: %s", id, form)
					}
				}
				if !found {
					t.Errorf("missing edit form for forum %d", id)
				}
			}
			locale := turkish
			if tag == "en" {
				locale = englishPreview
			}
			for _, key := range []string{"node.create_category", "node.create_forum", "node.allow_polls", "node.delete", "node.confirm"} {
				if !strings.Contains(body, locale.Text(key)) {
					t.Errorf("missing %s", key)
				}
			}
			csrf := mustFindCSRF(t, admin, path)
			resp, err := admin.PostForm(path, url.Values{"csrf": {csrf}, "action": {"create-category"}, "title": {"x"}, "slug": {"invalid"}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 400 || !strings.Contains(string(data), locale.Text("node.bad_fields")) {
				t.Fatalf("invalid node form: %d %s", resp.StatusCode, data)
			}
		})
	}
}
