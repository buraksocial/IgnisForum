package web

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"
)

// History is bounded and committed in the same primary record as its settings.
// It is an operational history, not a tamper-proof or exhaustive security log.
const settingsHistoryLimit = 100

// Public rendering needs current preferences, not decoded history entries.
// Skip allocating their strings/timestamps on every forum request.
type discardedSettingsHistory struct{}

func (*discardedSettingsHistory) UnmarshalJSON([]byte) error { return nil }

func sameAppearance(a, b appearanceSettings) bool {
	return a.themeDesign == b.themeDesign && a.Statistics == b.Statistics &&
		a.Presence == b.Presence && a.Media == b.Media && a.Attachments == b.Attachments &&
		slices.Equal(a.Pinned, b.Pinned) && a.AdTitle == b.AdTitle && a.AdURL == b.AdURL
}

type settingsEvent struct {
	AdminID uint64
	At      time.Time
	Action  string
	Target  string
}

func appendSettingsEvent(events []settingsEvent, adminID uint64, action, target string) []settingsEvent {
	if len(events) >= settingsHistoryLimit {
		events = events[len(events)-settingsHistoryLimit+1:]
	}
	return append(events, settingsEvent{AdminID: adminID, At: time.Now().UTC(), Action: action, Target: target})
}

type pluginPreferences struct {
	Disabled map[string]bool
	History  []settingsEvent `json:",omitempty"`
}

// Read the original bare disabled-ID map as well as the versioned envelope.
// New writes use the envelope so state and its event cannot diverge in a crash.
func (srv *Server) readPluginPreferences() (pluginPreferences, error) {
	return srv.readPluginPreferencesRecord(true)
}

func (srv *Server) readPluginPreferencesRecord(withHistory bool) (pluginPreferences, error) {
	raw, exists := srv.Store.GetRaw(pluginSettingsKey)
	if !exists {
		return pluginPreferences{}, nil
	}
	var settings pluginPreferences
	var envelope struct {
		Version int `json:"version"`
		pluginPreferences
	}
	var target any = &envelope
	if !withHistory {
		target = &struct {
			Version *int `json:"version"`
			*pluginPreferences
			History discardedSettingsHistory
		}{Version: &envelope.Version, pluginPreferences: &envelope.pluginPreferences}
	}
	if err := json.Unmarshal(raw, target); err == nil && envelope.Version == 1 {
		if envelope.Disabled == nil {
			return pluginPreferences{}, errors.New("plugin preferences: missing disabled map")
		}
		return envelope.pluginPreferences, nil
	}
	// Decoding as a boolean map also rejects unknown/malformed envelopes.
	if err := json.Unmarshal(raw, &settings.Disabled); err != nil {
		return pluginPreferences{}, err
	}
	return settings, nil
}

func (srv *Server) writePluginPreferences(settings pluginPreferences) error {
	return srv.Store.PutJSON(pluginSettingsKey, struct {
		Version int `json:"version"`
		pluginPreferences
	}{Version: 1, pluginPreferences: settings})
}

func recentSettingsEvents(appearance, plugins []settingsEvent) []settingsEvent {
	events := make([]settingsEvent, 0, len(appearance)+len(plugins))
	events = append(events, appearance...)
	events = append(events, plugins...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	if len(events) > 20 {
		events = events[:20]
	}
	return events
}
