package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProductionSafeDefaults(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr != "127.0.0.1:8090" {
		t.Fatalf("default listen address = %q; want loopback-only", cfg.ListenAddr)
	}
	if !cfg.SecureCookies {
		t.Fatal("secure cookies must be enabled by default")
	}
	if cfg.PreviewLocale != "" {
		t.Fatal("incomplete locales must not be enabled by default")
	}
}

func TestPreviewLocaleConfig(t *testing.T) {
	for _, tag := range []string{"en", "fr"} {
		t.Run(tag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("preview_locale: "+tag+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tag == "en" && (err != nil || cfg.PreviewLocale != tag) {
				t.Fatalf("preview locale = %q, error = %v", cfg.PreviewLocale, err)
			}
			if tag == "fr" && err == nil {
				t.Fatal("unsupported preview locale accepted")
			}
		})
	}
}
