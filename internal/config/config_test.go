package config

import "testing"

func TestProductionSafeDefaults(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr != "127.0.0.1:8090" {
		t.Fatalf("default listen address = %q; want loopback-only", cfg.ListenAddr)
	}
	if !cfg.SecureCookies {
		t.Fatal("secure cookies must be enabled by default")
	}
}
