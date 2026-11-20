package flarum

import (
	"strings"
	"testing"
)

func TestSafePostHTMLKeepsFormattingAndRemovesActiveContent(t *testing.T) {
	got := SafePostHTML(`<p>Hello <strong>forum</strong> <a href="https://example.test/path?a=1&amp;b=2" onclick="alert(1)">link</a></p><script>alert(2)</script><img src=x onerror=alert(3)><a href="javascript:alert(4)">bad</a>`)
	for _, want := range []string{"<p>", "<strong>forum</strong>", `href="https://example.test/path?a=1&amp;b=2"`, `rel="nofollow noopener noreferrer"`, "link"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitized HTML missing %q: %s", want, got)
		}
	}
	for _, bad := range []string{"<script", "alert(2)", "onclick", "onerror", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("sanitized HTML retained %q: %s", bad, got)
		}
	}
}

func TestSafePostHTMLEscapesMalformedAndUnknownContent(t *testing.T) {
	got := SafePostHTML(`<p>safe <custom onclick="x">text</custom></p><iframe src="https://evil.test">secret</iframe>`)
	if !strings.Contains(got, "safe text") || strings.Contains(got, "secret") || strings.Contains(got, "custom") {
		t.Fatalf("unexpected sanitized output: %s", got)
	}
}
