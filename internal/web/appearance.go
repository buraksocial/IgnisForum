package web

import (
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
)

const appearanceKey = "settings:appearance"
const pluginSettingsKey = "settings:plugins-disabled"

type appearanceSettings struct {
	themeDesign
	PreviousTheme               *themeDesign `json:",omitempty"`
	Statistics, Presence, Media bool
	Attachments                 bool
	Pinned                      []uint64
	AdTitle, AdURL              string
}

func (srv *Server) appearance() appearanceSettings {
	settings, err := srv.readAppearance()
	if err != nil {
		return appearanceSettings{themeDesign: themeDesign{DefaultTheme: "thred"}}
	}
	return settings
}

func (srv *Server) readAppearance() (appearanceSettings, error) {
	settings := appearanceSettings{themeDesign: themeDesign{DefaultTheme: "thred"}, Statistics: true}
	if _, err := srv.Store.GetJSON(appearanceKey, &settings); err != nil {
		return appearanceSettings{}, err
	}
	settings.themeDesign = settings.themeDesign.sanitized()
	if _, _, ok := srv.themes.Get(settings.DefaultTheme); !ok {
		settings.DefaultTheme = "thred"
	}
	return settings, nil
}

func (srv *Server) adminAppearance(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "Görünüm ve eklentiler", "admin", nil)
	if theme, _, ok := srv.themes.Get(r.URL.Query().Get("preview")); ok {
		d.ThemeID, d.ThemeVersion = theme.ID, theme.Version
	}
	srv.renderAppearance(w, r, d)
}

func (srv *Server) renderAppearance(w http.ResponseWriter, r *http.Request, d *pageData) {
	w.Header().Set("Cache-Control", "private, no-store")
	disabled, err := srv.disabledPlugins()
	if err != nil {
		http.Error(w, "Eklenti ayarları okunamadı.", http.StatusInternalServerError)
		return
	}
	d.Extensions = srv.D.Plugins.ConfiguredStatus(disabled)
	srv.render(w, "admin_appearance", d)
}

func (srv *Server) disabledPlugins() (map[string]bool, error) {
	var disabled map[string]bool
	_, err := srv.Store.GetJSON(pluginSettingsKey, &disabled)
	return disabled, err
}

func (srv *Server) savePluginState(w http.ResponseWriter, r *http.Request) {
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.PostForm.Get("csrf")) {
		srv.deny(w, r, "Form doğrulanamadı.")
		return
	}
	id, action := chi.URLParam(r, "pluginID"), r.PostForm.Get("action")
	if action != "enable" && action != "disable" {
		http.Error(w, "Geçersiz eklenti işlemi.", http.StatusBadRequest)
		return
	}
	found := false
	for _, status := range srv.D.Plugins.Status() {
		if status.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if ok, retry := srv.D.RL.Allow("plugin-state", itoa(admin.ID), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	if err := srv.setPluginDisabled(id, action == "disable"); err != nil {
		http.Error(w, "Eklenti ayarı kaydedilemedi.", http.StatusInternalServerError)
		return
	}
	log.Printf("plugin %s action=%s admin=%d", id, action, admin.ID)
	http.Redirect(w, r, "/admin/appearance#wasm", http.StatusSeeOther)
}

func (srv *Server) setPluginDisabled(id string, value bool) error {
	srv.pluginSettingsMu.Lock()
	defer srv.pluginSettingsMu.Unlock()
	disabled, err := srv.disabledPlugins()
	if err != nil {
		return err
	}
	if disabled == nil {
		disabled = make(map[string]bool)
	}
	if value {
		disabled[id] = true
	} else {
		delete(disabled, id)
	}
	return srv.Store.PutJSON(pluginSettingsKey, disabled)
}

func (srv *Server) saveAppearance(w http.ResponseWriter, r *http.Request) {
	admin := srv.requireAdmin(w, r)
	if admin == nil {
		return
	}
	if err := r.ParseForm(); err != nil || !csrfOK(r, r.PostForm.Get("csrf")) {
		srv.deny(w, r, "Form doğrulanamadı.")
		return
	}
	if ok, retry := srv.D.RL.Allow("appearance", itoa(admin.ID), 20, time.Minute); !ok {
		srv.tooMany(w, r, retry)
		return
	}
	intent := r.PostForm.Get("intent")
	if intent == "restore" {
		srv.restoreTheme(w, r, admin.ID)
		return
	}
	if intent != "" && intent != "save" && intent != "preview" {
		http.Error(w, "Geçersiz görünüm işlemi.", http.StatusBadRequest)
		return
	}
	design, err := srv.parseThemeDesign(r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s := appearanceSettings{
		themeDesign: design, Statistics: r.PostForm.Get("statistics") == "1",
		Presence: r.PostForm.Get("presence") == "1", Media: r.PostForm.Get("media") == "1",
		Attachments: r.PostForm.Get("attachments") == "1",
		AdTitle:     strings.TrimSpace(r.PostForm.Get("ad_title")), AdURL: strings.TrimSpace(r.PostForm.Get("ad_url")),
	}
	pins := strings.FieldsFunc(r.PostForm.Get("pinned"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	if len(pins) > 8 || utf8.RuneCountInString(s.AdTitle) > 100 || (s.AdTitle == "") != (s.AdURL == "") || (s.AdURL != "" && !externalHTTPS(s.AdURL)) {
		http.Error(w, "En fazla 8 duyuru ve geçerli bir HTTPS reklam bağlantısı kullanın.", http.StatusBadRequest)
		return
	}
	seen := make(map[uint64]bool)
	for _, pin := range pins {
		id := parseID(pin)
		if th, ok := srv.Repos.Threads.Get(id); id == 0 || !ok || th.Status != domain.ThreadOpen && th.Status != domain.ThreadClosed || th.DiscussionState != 0 {
			http.Error(w, "Duyuru için geçerli bir konu numarası girin.", http.StatusBadRequest)
			return
		}
		if !seen[id] {
			s.Pinned = append(s.Pinned, id)
			seen[id] = true
		}
	}
	if intent == "preview" {
		d := srv.base(r, "Görünüm ve eklentiler", "admin", nil)
		s.PreviousTheme = d.Appearance.PreviousTheme
		d.Appearance, d.ThemeDraft = s, true
		theme, _, _ := srv.themes.Get(s.DefaultTheme)
		d.ThemeID, d.ThemeVersion = theme.ID, theme.Version
		srv.renderAppearance(w, r, d)
		return
	}
	if err := srv.persistAppearance(s); err != nil {
		http.Error(w, "Ayarlar kaydedilemedi.", http.StatusInternalServerError)
		return
	}
	log.Printf("appearance settings updated by admin=%d", admin.ID)
	http.Redirect(w, r, "/admin/appearance", http.StatusSeeOther)
}

func (srv *Server) persistAppearance(s appearanceSettings) error {
	srv.appearanceMu.Lock()
	defer srv.appearanceMu.Unlock()
	current, err := srv.readAppearance()
	if err != nil {
		return err
	}
	s.PreviousTheme = current.PreviousTheme
	if current.themeDesign != s.themeDesign {
		s.PreviousTheme = &current.themeDesign
	}
	return srv.Store.PutJSON(appearanceKey, &s)
}

func (srv *Server) restoreTheme(w http.ResponseWriter, r *http.Request, adminID uint64) {
	srv.appearanceMu.Lock()
	s, err := srv.readAppearance()
	status := http.StatusInternalServerError
	if err == nil {
		status = http.StatusConflict
		if previous := s.PreviousTheme; previous != nil {
			if _, _, installed := srv.themes.Get(previous.DefaultTheme); installed {
				s.themeDesign, s.PreviousTheme = previous.sanitized(), nil
				status = http.StatusSeeOther
				if err = srv.Store.PutJSON(appearanceKey, &s); err != nil {
					status = http.StatusInternalServerError
				}
			}
		}
	}
	srv.appearanceMu.Unlock()
	switch status {
	case http.StatusSeeOther:
		log.Printf("previous theme restored by admin=%d", adminID)
		http.Redirect(w, r, "/admin/appearance", status)
	case http.StatusConflict:
		http.Error(w, "Geri alınabilir önceki tema ayarı yok veya tema artık kurulu değil.", status)
	default:
		http.Error(w, "Tema ayarları geri alınamadı.", status)
	}
}

func externalHTTPS(raw string) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

type sidebarView struct {
	Pinned                 []threadRow
	Statistics, Presence   bool
	Threads, Posts, Online int
	Active                 []presenceMember
	AdTitle, AdURL         string
}

func (srv *Server) homeSidebar(r *http.Request, d *pageData) sidebarView {
	s := d.Appearance
	v := sidebarView{Statistics: s.Statistics, Presence: s.Presence, AdTitle: s.AdTitle, AdURL: s.AdURL}
	for _, id := range s.Pinned {
		th, ok := srv.Repos.Threads.Get(id)
		if ok && (th.Status == domain.ThreadOpen || th.Status == domain.ThreadClosed) && th.DiscussionState == 0 && srv.can(r, d.Me, perm.ScopeNode, th.NodeID, perm.KeyForumView) {
			v.Pinned = append(v.Pinned, threadRow{ID: th.ID, Title: th.Title})
		}
	}
	if s.Statistics {
		for _, category := range d.Cats {
			for _, forum := range category.Forums {
				v.Threads += forum.ThreadCount
				v.Posts += forum.PostCount
			}
		}
	}
	if s.Presence {
		v.Online, v.Active = srv.presence.snapshot(d.Now, d.Me != nil && d.Me.State == 0)
		for i := range v.Active {
			v.Active[i].Name = srv.username(v.Active[i].ID)
		}
	}
	return v
}

const presenceWindow = 15 * time.Minute
const presenceLimit = 8192

type presenceMember struct {
	ID   uint64
	Name string
	at   time.Time
}
type memberPresence struct {
	mu        sync.Mutex
	seen      map[uint64]time.Time
	nextSweep time.Time
}

func (p *memberPresence) touch(id uint64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = make(map[uint64]time.Time)
	}
	if !now.Before(p.nextSweep) {
		for uid, at := range p.seen {
			if now.Sub(at) >= presenceWindow {
				delete(p.seen, uid)
			}
		}
		p.nextSweep = now.Add(time.Minute)
	}
	if at, tracked := p.seen[id]; (tracked && now.After(at)) || (!tracked && len(p.seen) < presenceLimit) {
		p.seen[id] = now
	}
}

func (p *memberPresence) snapshot(now time.Time, names bool) (int, []presenceMember) {
	p.mu.Lock()
	count := 0
	var active []presenceMember
	for id, at := range p.seen {
		if now.Sub(at) >= presenceWindow {
			continue
		}
		count++
		if names && now.Sub(at) < 5*time.Minute {
			active = append(active, presenceMember{ID: id, at: at})
		}
	}
	p.mu.Unlock()
	sort.Slice(active, func(i, j int) bool {
		if active[i].at.Equal(active[j].at) {
			return active[i].ID < active[j].ID
		}
		return active[i].at.After(active[j].at)
	})
	if len(active) > 12 {
		active = active[:12]
	}
	return count, active
}
