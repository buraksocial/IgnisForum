package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/buraksocial/IgnisForum/internal/plugin"
)

func (srv *Server) setPluginConfiguration(id string, raw []byte, adminID uint64) error {
	config, err := plugin.NormalizeConfig(raw)
	if err != nil {
		return err
	}
	srv.pluginSettingsMu.Lock()
	defer srv.pluginSettingsMu.Unlock()
	settings, err := srv.readPluginPreferences()
	if err != nil {
		return err
	}
	if bytes.Equal(config, settings.Config[id]) {
		return nil
	}
	if len(config) == 0 {
		delete(settings.Config, id)
	} else {
		if settings.Config == nil {
			settings.Config = make(map[string]json.RawMessage)
		}
		if len(settings.Config) >= 16 && settings.Config[id] == nil {
			return errors.New("plugin configuration count limit reached")
		}
		settings.Config[id] = config
	}
	if settings.Disabled == nil {
		settings.Disabled = make(map[string]bool)
	}
	settings.History = appendSettingsEvent(settings.History, adminID, "history.plugin_configure", id)
	return srv.writePluginPreferences(settings)
}

// Called only after requireAdmin, CSRF validation, package lookup and throttling.
func (srv *Server) pluginConfiguration(w http.ResponseWriter, r *http.Request, adminID uint64, id, action string) {
	w.Header().Set("Cache-Control", "private, no-store")
	locale := srv.uiLocale()
	config, err := plugin.NormalizeConfig([]byte(r.PostForm.Get("configuration")))
	if err != nil {
		http.Error(w, locale.Text("appearance.bad_config"), http.StatusBadRequest)
		return
	}
	if action == "configure" {
		if err := srv.setPluginConfiguration(id, config, adminID); err != nil {
			http.Error(w, locale.Text("appearance.plugin_save_failed"), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/appearance#wasm", http.StatusSeeOther)
		return
	}
	preferences, err := srv.readPluginPreferencesRecord(false)
	if err != nil {
		http.Error(w, locale.Text("appearance.read_failed"), http.StatusInternalServerError)
		return
	}
	result, err := srv.D.Plugins.PreviewHook(r.Context(), id, "home.decorate", map[string]any{"version": Version, "categories": 0}, preferences.Disabled, config)
	if errors.Is(err, plugin.ErrPreviewDependency) {
		http.Error(w, locale.Text("appearance.preview_blocked"), http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("plugin preview id=%s admin=%d: %v", id, adminID, err)
		http.Error(w, locale.Text("appearance.preview_failed"), http.StatusUnprocessableEntity)
		return
	}
	d := srv.base(r, locale.Text("admin.appearance"), "admin", nil)
	d.PluginPreviewTarget, d.PluginPreviewConfig = id, string(config)
	if d.PluginPreviewConfig == "" {
		d.PluginPreviewConfig = "{}"
	}
	d.PluginPreviewNotices = pluginNotices([][]byte{result})
	srv.renderAppearance(w, r, d)
}

// Both private previews and public hooks use the same bounded plain-text view.
func pluginNotices(results [][]byte) []string {
	var notices []string
	for _, result := range results {
		var response struct {
			Notices []string `json:"notices"`
		}
		if json.Unmarshal(result, &response) != nil {
			continue
		}
		for _, notice := range response.Notices {
			notice = strings.TrimSpace(notice)
			if notice != "" && len(notice) <= 240 && len(notices) < 5 {
				notices = append(notices, notice)
			}
		}
	}
	return notices
}
