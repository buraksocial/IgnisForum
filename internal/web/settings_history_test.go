package web

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	assets "github.com/buraksocial/IgnisForum"
	"github.com/buraksocial/IgnisForum/internal/store"
)

func TestSettingsHistorySurvivesReopen(t *testing.T) {
	cfg := store.OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20}
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	themes, err := LoadThemeManager(assets.FS, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: st, themes: themes}
	// Old installations stored a bare boolean map, including IDs that could
	// resemble new field names. Its preferences must not be silently dropped.
	if err := st.PutRaw(pluginSettingsKey, []byte(`{"legacy":true,"Disabled":true,"version":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := srv.setPluginDisabled("next", true, 42); err != nil {
		t.Fatal(err)
	}
	wantPlugin, err := srv.readPluginPreferences()
	if err != nil || !wantPlugin.Disabled["legacy"] || !wantPlugin.Disabled["Disabled"] || !wantPlugin.Disabled["version"] || len(wantPlugin.History) != 1 {
		t.Fatalf("legacy migration: %+v, %v", wantPlugin, err)
	}
	settings := appearanceSettings{themeDesign: themeDesign{DefaultTheme: "ember"}, Media: true}
	if err := srv.persistAppearance(settings, 42); err != nil {
		t.Fatal(err)
	}
	wantAppearance, err := srv.readAppearance()
	if err != nil || len(wantAppearance.History) != 1 {
		t.Fatalf("appearance history: %+v %v", wantAppearance, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv = &Server{Store: st, themes: themes}
	gotPlugin, err := srv.readPluginPreferences()
	if err != nil || !reflect.DeepEqual(gotPlugin, wantPlugin) {
		t.Fatalf("plugin reopen: %+v %v", gotPlugin, err)
	}
	gotAppearance, err := srv.readAppearance()
	if err != nil || !reflect.DeepEqual(gotAppearance, wantAppearance) {
		t.Fatalf("appearance reopen: %+v %v", gotAppearance, err)
	}
	public := srv.appearance()
	if !sameAppearance(public, wantAppearance) || len(public.History) != 0 {
		t.Fatalf("public preferences decoded history or lost state: %+v", public)
	}
	disabled, err := srv.disabledPlugins()
	if err != nil || !reflect.DeepEqual(disabled, wantPlugin.Disabled) {
		t.Fatalf("public plugin preferences: %+v %v", disabled, err)
	}
}

func TestSettingsHistoryBoundsAndConcurrentPluginWrites(t *testing.T) {
	_, st, _, _, _ := newTestServer(t)
	srv := &Server{Store: st}
	var wg sync.WaitGroup
	for i := 0; i < 120; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := srv.setPluginDisabled(fmt.Sprintf("plugin-%d", i), true, uint64(i+1)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	settings, err := srv.readPluginPreferences()
	if err != nil || len(settings.Disabled) != 120 || len(settings.History) != settingsHistoryLimit {
		t.Fatalf("concurrent writes lost settings or history: %d %d %v", len(settings.Disabled), len(settings.History), err)
	}
	for _, event := range settings.History {
		if !settings.Disabled[event.Target] || event.AdminID == 0 {
			t.Errorf("invalid event %+v", event)
		}
	}
	old := []settingsEvent{{At: time.Unix(1, 0)}}
	newer := []settingsEvent{{At: time.Unix(2, 0)}}
	merged := recentSettingsEvents(old, newer)
	if len(merged) != 2 || !merged[0].At.Equal(newer[0].At) || !old[0].At.Equal(time.Unix(1, 0)) {
		t.Fatal("history sort mutated source or is unordered")
	}
	if got := len(recentSettingsEvents(settings.History, settings.History)); got != 20 {
		t.Fatalf("display limit: %d", got)
	}
}

func TestMalformedPluginPreferencesFailClosed(t *testing.T) {
	_, st, _, _, _ := newTestServer(t)
	srv := &Server{Store: st}
	for _, raw := range []string{`{"version":2,"Disabled":{"base":true}}`, `{"version":1,"Disabled":"invalid"}`, `{"version":1}`, `not-json`} {
		if err := st.PutRaw(pluginSettingsKey, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := srv.setPluginDisabled("base", false, 42); err == nil {
			t.Fatalf("invalid preferences accepted: %s", raw)
		}
		if _, err := srv.disabledPlugins(); err == nil {
			t.Fatalf("public reader accepted invalid preferences: %s", raw)
		}
		after, _ := st.GetRaw(pluginSettingsKey)
		if string(after) != raw {
			t.Fatal("corrupt preferences overwritten")
		}
	}
}
