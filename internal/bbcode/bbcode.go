// Package bbcode renders the forum's message markup to safe HTML. M1 ships a
// small, safe core (HTML-escaped, whitelist of inline tags); the full BBCode +
// WYSIWYG editor is M3. Slugs are slugified here for the SEO route table.
package bbcode

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

var (
	reB     = regexp.MustCompile(`\[b\](.*?)\[/b\]`)
	reI     = regexp.MustCompile(`\[i\](.*?)\[/i\]`)
	reU     = regexp.MustCompile(`\[u\](.*?)\[/u\]`)
	reS     = regexp.MustCompile(`\[s\](.*?)\[/s\]`)
	reURL   = regexp.MustCompile(`\[url=(https?://[^\s\]]+)\](.*?)\[/url\]`)
	reCode  = regexp.MustCompile(`(?s)\[code\](.*?)\[/code\]`)
	reQuote = regexp.MustCompile(`(?s)\[quote\](.*?)\[/quote\]`)
	reSlug  = regexp.MustCompile(`[^\p{L}\p{N}-]+`)
)

// Render converts a raw BBCode message to an HTML fragment. All user text is
// escaped first, so arbitrary HTML is impossible; only the whitelist of tags we
// re-introduce is trusted. Newlines become <br>.
func Render(raw string) string {
	s := html.EscapeString(raw)

	s = reCode.ReplaceAllString(s, `<pre class="code">$1</pre>`)
	s = reB.ReplaceAllString(s, `<strong>$1</strong>`)
	s = reI.ReplaceAllString(s, `<em>$1</em>`)
	s = reU.ReplaceAllString(s, `<u>$1</u>`)
	s = reS.ReplaceAllString(s, `<s>$1</s>`)
	s = reURL.ReplaceAllString(s, `<a href="$1" rel="nofollow noopener" target="_blank">$2</a>`)
	s = reQuote.ReplaceAllString(s, `<blockquote class="quote">$1</blockquote>`)

	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

// Slug turns a title into a URL-safe slug (ASCII-folding-free: Turkish letters
// are kept, other non-alphanumerics become '-').
func Slug(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// IsURLSafe reports whether s is a valid slug character set.
func IsURLSafe(s string) bool {
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
			return false
		}
	}
	return len(s) > 0
}
