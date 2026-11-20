package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type mediaItem struct{ Kind, URL, Host, Preview string }

var mediaMarkup = regexp.MustCompile(`(?is)\[code\].*?\[/code\]|\[(img|link|download)\](https://[^\s\[\]]+)\[/(img|link|download)\]`)

func postMedia(raw string) []mediaItem {
	var result []mediaItem
	for _, m := range mediaMarkup.FindAllStringSubmatch(raw, 20) {
		kind := strings.ToLower(m[1])
		if kind == "" || kind != strings.ToLower(m[3]) || !externalHTTPS(m[2]) {
			continue
		}
		u, _ := url.Parse(m[2])
		item := mediaItem{Kind: kind, URL: m[2], Host: u.Host}
		if kind == "img" {
			item.Preview = "/media/preview?url=" + url.QueryEscape(m[2])
		}
		result = append(result, item)
		if len(result) == 6 {
			break
		}
	}
	return result
}

// The server never fetches the image. The browser loads it only after the
// visitor opens the dedicated preview, without sending a forum referrer.
func (srv *Server) mediaPreview(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if !srv.appearance().Media || !externalHTTPS(raw) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src https:; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	srv.render(w, "media_preview", &pageData{Locale: srv.uiLocale(), PreviewURL: raw})
}
