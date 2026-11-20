package release

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckSignedFeed(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	policy := Policy{Channel: "stable", Installed: "1.2.3", Platform: "linux/amd64", Now: now}
	makeFeed := func(channel, version string, expiry time.Time, tampered bool) []byte {
		t.Helper()
		m := Metadata{Schema: 1, Product: "IgnisForum", Channel: channel, Version: version, Platform: policy.Platform, Issued: now.Add(-time.Hour), Expires: expiry, Size: 10, SHA256: strings.Repeat("a", 64)}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		sig := ed25519.Sign(key, raw)
		if tampered {
			raw = append(raw, ' ')
		}
		body, err := json.Marshal(signedFeed{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(sig)})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	expires := now.Add(time.Hour)
	feeds := map[string][]byte{
		"/stable": makeFeed("stable", "1.2.4", expires, false), "/security": makeFeed("security", "1.3.0", expires, false),
		"/equal": makeFeed("stable", "1.2.3", expires, false), "/older": makeFeed("stable", "1.2.2", expires, false),
		"/expired": makeFeed("stable", "1.2.3", now.Add(-time.Minute), false), "/tampered": makeFeed("stable", "1.2.4", expires, true),
		"/invalid": []byte(`{"metadata":"abc","signature":"def","artifact":"https://example.com/"}`),
	}
	var redirected atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/redirect-target", http.StatusFound)
		case "/redirect-target":
			redirected.Add(1)
		case "/large":
			w.(http.Flusher).Flush() // stream without Content-Length
			_, _ = w.Write([]byte(strings.Repeat("x", MaxFeed+1)))
		case "/encoding":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("ignored"))
		default:
			if feed, ok := feeds[r.URL.Path]; ok {
				_, _ = w.Write(feed)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	for _, tc := range []struct {
		path, channel, version, errorPart string
		update                            bool
	}{
		{"/stable", "stable", "1.2.4", "", true}, {"/security", "security", "1.3.0", "", true},
		{"/equal", "stable", "1.2.3", "", false}, {"/older", "stable", "1.2.2", "", false},
		{"/security", "stable", "", "channel", false}, {"/expired", "stable", "", "expired", false},
		{"/tampered", "stable", "", "signature", false}, {"/invalid", "stable", "", "feed JSON", false},
		{"/redirect", "stable", "", "HTTP 302", false}, {"/missing", "stable", "", "HTTP 404", false},
		{"/large", "stable", "", "size limit", false}, {"/encoding", "stable", "", "encoding", false},
	} {
		p := policy
		p.Channel = tc.channel
		res, err := checkFeed(t.Context(), srv.URL+tc.path, pub, p, transport)
		if tc.errorPart != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errorPart) {
				t.Errorf("%s: wanted %q error, got %v", tc.path, tc.errorPart, err)
			}
			continue
		}
		if err != nil || res.Channel != p.Channel || res.Installed != p.Installed || res.Version != tc.version || res.UpdateAvailable != tc.update || res.ArtifactVerified {
			t.Errorf("%s: result=%+v error=%v", tc.path, res, err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("feed redirect followed")
	}
	if _, err := CheckFeed(t.Context(), srv.URL+"/stable", pub, policy); err == nil {
		t.Fatal("production client trusted an unprovisioned TLS certificate")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := checkFeed(ctx, srv.URL+"/stable", pub, policy, transport); err == nil {
		t.Fatal("canceled request succeeded")
	}
	for _, endpoint := range []string{"http://example.org/release", "https://user:pass@example.org/", srv.URL + "/stable?", srv.URL + "/stable?token=x", srv.URL + "/stable#tag"} {
		if _, err := CheckFeed(t.Context(), endpoint, pub, policy); err == nil {
			t.Fatal("accepted prohibited URL")
		}
	}
}
