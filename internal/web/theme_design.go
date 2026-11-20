package web

import (
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// These fields are embedded in appearanceSettings to retain the original
// top-level DefaultTheme and Accent JSON fields in existing installations.
type themeDesign struct {
	DefaultTheme string
	Accent       string
	Font         string
	SiteWidth    string
	Radius       string
}

var themeHexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func themeFont(value string) string {
	switch value {
	case "system":
		return "system-ui,sans-serif"
	case "serif":
		return "Georgia,serif"
	case "mono":
		return "ui-monospace,monospace"
	default:
		return ""
	}
}

func themeNumber(value string, min, max int) (string, bool) {
	if value == "" {
		return "", true
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max || strconv.Itoa(n) != value {
		return "", false
	}
	return value, true
}

func (srv *Server) parseThemeDesign(form url.Values) (themeDesign, error) {
	d := themeDesign{
		DefaultTheme: form.Get("default_theme"), Accent: strings.TrimSpace(form.Get("accent")),
		Font: form.Get("theme_font"), SiteWidth: strings.TrimSpace(form.Get("site_width")),
		Radius: strings.TrimSpace(form.Get("corner_radius")),
	}
	if _, _, ok := srv.themes.Get(d.DefaultTheme); !ok {
		return d, fmt.Errorf("geçersiz tema")
	}
	if d.Accent != "" && !themeHexColor.MatchString(d.Accent) {
		return d, fmt.Errorf("vurgu rengini #RRGGBB biçiminde girin")
	}
	if d.Font != "" && themeFont(d.Font) == "" {
		return d, fmt.Errorf("geçersiz yazı tipi")
	}
	if _, ok := themeNumber(d.SiteWidth, 960, 1800); !ok {
		return d, fmt.Errorf("site genişliği 960–1800 piksel arasında bir tam sayı olmalı")
	}
	if _, ok := themeNumber(d.Radius, 0, 24); !ok {
		return d, fmt.Errorf("köşe yarıçapı 0–24 piksel arasında bir tam sayı olmalı")
	}
	d.Accent = strings.ToUpper(d.Accent)
	return d, nil
}

func (d themeDesign) sanitized() themeDesign {
	if !themeHexColor.MatchString(d.Accent) {
		d.Accent = ""
	}
	if themeFont(d.Font) == "" {
		d.Font = ""
	}
	d.SiteWidth, _ = themeNumber(d.SiteWidth, 960, 1800)
	d.Radius, _ = themeNumber(d.Radius, 0, 24)
	return d
}

// CSS generates declarations, never accepting CSS from the form or database.
// Revalidate at this boundary before marking the result as trusted CSS.
func (d themeDesign) CSS() template.CSS {
	d = d.sanitized()
	var b strings.Builder
	if d.Accent != "" {
		fmt.Fprintf(&b, "--color-brand:%s;--color-brand-contrast:%s;", d.Accent, accentForeground(d.Accent))
	}
	if font := themeFont(d.Font); font != "" {
		fmt.Fprintf(&b, "--font-body:%s;--font-heading:%s;", font, font)
	}
	if d.SiteWidth != "" {
		fmt.Fprintf(&b, "--max-width-site:%spx;", d.SiteWidth)
	}
	if d.Radius != "" {
		fmt.Fprintf(&b, "--radius-theme:%spx;", d.Radius)
	}
	return template.CSS(b.String())
}

// Choose whichever of black or white has the higher WCAG contrast ratio on
// the accent fill. This does not guarantee accent link contrast on all themes.
func accentForeground(hex string) string {
	var luminance float64
	for i, weight := range []float64{0.2126, 0.7152, 0.0722} {
		n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		v := float64(n) / 255
		if v <= 0.04045 {
			v /= 12.92
		} else {
			v = math.Pow((v+0.055)/1.055, 2.4)
		}
		luminance += weight * v
	}
	if (luminance+0.05)/0.05 >= 1.05/(luminance+0.05) {
		return "#000000"
	}
	return "#FFFFFF"
}
