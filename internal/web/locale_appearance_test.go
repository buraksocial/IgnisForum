package web

import (
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestAppearanceValidationLocales(t *testing.T) {
	for _, tag := range []string{"", "en"} {
		t.Run("locale="+tag, func(t *testing.T) {
			ts, st, _, authSvc, _ := newTestServerWithDeps(t, Deps{PreviewLocale: tag})
			admin := loginAsAdmin(t, ts, st, authSvc)
			locale := turkish
			if tag == "en" {
				locale = englishPreview
			}
			path := ts.URL + "/admin/appearance"
			csrf := mustFindCSRF(t, admin, path)
			for _, tc := range []struct {
				field, value, key string
				status            int
			}{
				{"default_theme", "missing", "appearance.bad_theme", 400},
				{"accent", "#fff;color:red", "appearance.bad_accent", 400},
				{"theme_font", "remote", "appearance.bad_font", 400},
				{"site_width", "1801", "appearance.bad_width", 400},
				{"corner_radius", "-1", "appearance.bad_radius", 400},
				{"intent", "unknown", "appearance.bad_action", 400},
				{"intent", "restore", "appearance.no_restore", 409},
				{"pinned", "999999", "appearance.bad_pin", 400},
				{"ad_url", "http://example.org", "appearance.bad_ad", 400},
				{"csrf", "wrong", "appearance.bad_form", 403},
			} {
				form := url.Values{"csrf": {csrf}, "default_theme": {"thred"}, "intent": {"save"}}
				form.Set(tc.field, tc.value)
				resp, err := admin.PostForm(path, form)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != tc.status || !strings.Contains(string(data), locale.Text(tc.key)) {
					t.Errorf("%s: status=%d body=%s error=%v", tc.key, resp.StatusCode, data, err)
				}
			}
			if raw, exists := st.GetRaw(appearanceKey); exists {
				t.Fatalf("rejected forms changed appearance: %s", raw)
			}
		})
	}
}
