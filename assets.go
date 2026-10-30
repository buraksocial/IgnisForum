// Package assets embeds the default theme (templates + styles) into the binary.
package assets

import "embed"

// FS holds templates/ and styles/. Paths inside: "templates/<name>.tmpl",
// "styles/thred/style.css".
//
//go:embed templates styles
var FS embed.FS
