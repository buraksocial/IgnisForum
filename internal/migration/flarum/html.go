// Package flarum contains safe, deterministic transforms for Flarum snapshots.
package flarum

import (
	stdhtml "html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

var allowedHTMLTags = map[string]string{
	"p": "p", "br": "br", "strong": "strong", "b": "strong",
	"em": "em", "i": "em", "u": "u", "s": "s", "del": "del",
	"blockquote": "blockquote", "pre": "pre", "code": "code",
	"ul": "ul", "ol": "ol", "li": "li", "a": "a",
}

var dropHTMLSubtrees = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true,
	"embed": true, "svg": true, "math": true, "template": true,
}

// SafePostHTML converts Flarum's stored rendered HTML into a small safe HTML
// subset accepted by IgnisForum post templates. Unknown tags are unwrapped;
// active-content subtrees and unsafe links are discarded.
func SafePostHTML(input string) string {
	doc, err := xhtml.Parse(strings.NewReader(input))
	if err != nil {
		return stdhtml.EscapeString(input)
	}
	var out strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && dropHTMLSubtrees[strings.ToLower(n.Data)] {
			return
		}
		if n.Type == xhtml.TextNode {
			out.WriteString(stdhtml.EscapeString(n.Data))
			return
		}
		if n.Type != xhtml.ElementNode {
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
			return
		}
		name := strings.ToLower(n.Data)
		tag, allowed := allowedHTMLTags[name]
		if !allowed {
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
			return
		}
		if tag == "a" {
			href := ""
			for _, attr := range n.Attr {
				if strings.EqualFold(attr.Key, "href") {
					href = safeHref(attr.Val)
					break
				}
			}
			if href == "" {
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
				return
			}
			out.WriteString(`<a href="` + stdhtml.EscapeString(href) + `" rel="nofollow noopener noreferrer">`)
		} else {
			out.WriteByte('<')
			out.WriteString(tag)
			out.WriteByte('>')
		}
		if tag != "br" {
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
			out.WriteString("</" + tag + ">")
		}
	}
	for child := doc.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}
	return out.String()
}

func safeHref(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return raw
	default:
		return ""
	}
}
