package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealthLoopbackAndSmokeMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/health", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"1.2.4"}`))
	}))
	defer srv.Close()
	if err := CheckHealth(context.Background(), HealthOptions{Endpoint: srv.URL + "/health", BearerToken: "secret", RequiredMarker: `"status":"ok"`}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://example.org/health", srv.URL + "/health?token=x", "http://user:pass@127.0.0.1/health", srv.URL + "/redirect"} {
		if err := CheckHealth(context.Background(), HealthOptions{Endpoint: bad, RequiredMarker: "ok"}); err == nil {
			t.Errorf("unsafe health endpoint accepted: %s", bad)
		}
	}
	if err := CheckHealth(context.Background(), HealthOptions{Endpoint: srv.URL + "/health", BearerToken: "wrong", RequiredMarker: "ok"}); err == nil {
		t.Fatal("invalid health token accepted")
	}
}
