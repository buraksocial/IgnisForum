package web

import (
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/buraksocial/IgnisForum/internal/domain"
	"github.com/buraksocial/IgnisForum/internal/perm"
)

const appearanceKey = "settings:appearance"

var accentColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type appearanceSettings struct {
	DefaultTheme                string
	Accent                      string
	Statistics, Presence, Media bool
	Pinned                      []uint64
	AdTitle, AdURL              string
}

func (srv *Server) appearance() appearanceSettings {
	settings := appearanceSettings{DefaultTheme: "thred", Statistics: true}
	if _, err := srv.Store.GetJSON(appearanceKey, &settings); err != nil {
		return appearanceSettings{DefaultTheme: "thred"}
	}
	if _, _, ok := srv.themes.Get(settings.DefaultTheme); !ok {
		settings.DefaultTheme = "thred"
	}
	if !accentColor.MatchString(settings.Accent) {
		settings.Accent = ""
	}
	return settings
}

func (srv *Server) adminAppearance(w http.ResponseWriter, r *http.Request) {
	if srv.requireAdmin(w, r) == nil {
		return
	}
	d := srv.base(r, "Görünüm ve eklentiler", "admin", nil)
	d.Extensions = srv.D.Plugins.Status()
	if theme, _, ok := srv.themes.Get(r.URL.Query().Get("preview")); ok {
		d.ThemeID, d.ThemeVersion = theme.ID, theme.Version
	}
	srv.render(w, "admin_appearance", d)
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
	s := appearanceSettings{
		DefaultTheme: r.PostForm.Get("default_theme"), Statistics: r.PostForm.Get("statistics") == "1",
		Presence: r.PostForm.Get("presence") == "1", Media: r.PostForm.Get("media") == "1",
		AdTitle: strings.TrimSpace(r.PostForm.Get("ad_title")), AdURL: strings.TrimSpace(r.PostForm.Get("ad_url")),
	}
	s.Accent = strings.TrimSpace(r.PostForm.Get("accent"))
	if s.Accent != "" && !accentColor.MatchString(s.Accent) {
		http.Error(w, "Vurgu rengini #RRGGBB biçiminde girin.", http.StatusBadRequest)
		return
	}
	if _, _, ok := srv.themes.Get(s.DefaultTheme); !ok {
		http.Error(w, "Geçersiz tema.", http.StatusBadRequest)
		return
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
	if err := srv.Store.PutJSON(appearanceKey, &s); err != nil {
		http.Error(w, "Ayarlar kaydedilemedi.", http.StatusInternalServerError)
		return
	}
	log.Printf("appearance settings updated by admin=%d", admin.ID)
	http.Redirect(w, r, "/admin/appearance", http.StatusSeeOther)
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
