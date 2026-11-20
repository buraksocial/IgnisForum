package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stageFixture(t *testing.T, now time.Time, payload []byte) StageOptions {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	m := Metadata{Schema: 1, Product: "IgnisForum", Channel: "stable", Version: "1.2.4", Platform: "linux/amd64", Size: int64(len(payload)), SHA256: hex.EncodeToString(hash[:]), Issued: now.Add(-time.Hour), Expires: now.Add(time.Hour)}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	return StageOptions{Metadata: raw, Signature: ed25519.Sign(key, raw), PublicKey: pub,
		Policy: Policy{Channel: "stable", Installed: "1.2.3", Platform: "linux/amd64"}, Directory: filepath.Join(parent, "candidate")}
}

func TestStageSignedArtifact(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("quarantined release bytes, never executed")
	var redirects atomic.Int32
	var fakeTime atomic.Int64
	fakeTime.Store(now.UnixNano())
	clock := func() time.Time { return time.Unix(0, fakeTime.Load()) }
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "" {
			t.Error("unexpected cookie or compression request")
		}
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/never", http.StatusFound)
		case "/never":
			redirects.Add(1)
		case "/encoding":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(payload)
		case "/corrupt":
			_, _ = w.Write(bytes.Repeat([]byte("x"), len(payload)))
		case "/short":
			w.(http.Flusher).Flush()
			_, _ = w.Write(payload[:len(payload)-1])
		case "/long":
			w.(http.Flusher).Flush()
			_, _ = w.Write(append(append([]byte(nil), payload...), 'x'))
		case "/partial":
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:5])
		case "/status":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/expires":
			fakeTime.Store(now.Add(2 * time.Hour).UnixNano())
			_, _ = w.Write(payload)
		case "/artifact":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	for _, tc := range []struct{ path, errorPart string }{
		{"/artifact", ""}, {"/redirect", "HTTP 302"}, {"/encoding", "encoding"},
		{"/corrupt", "mismatch"}, {"/short", "mismatch"}, {"/long", "mismatch"},
		{"/partial", "EOF"}, {"/status", "HTTP 503"}, {"/missing", "HTTP 404"}, {"/expires", "expired"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			fakeTime.Store(now.UnixNano())
			options := stageFixture(t, now, payload)
			options.ArtifactURL = srv.URL + tc.path
			res, err := stage(t.Context(), options, transport, clock)
			if tc.errorPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorPart) {
					t.Fatalf("wanted %q error, got %v", tc.errorPart, err)
				}
				if _, err := os.Lstat(options.Directory); !os.IsNotExist(err) {
					t.Fatalf("failed stage left files: %v", err)
				}
				return
			}
			if err != nil || !res.ArtifactVerified || res.Installed || res.Version != "1.2.4" || res.Channel != "stable" {
				t.Fatalf("result=%+v error=%v", res, err)
			}
			entries, err := os.ReadDir(res.Directory)
			if err != nil || len(entries) != 4 {
				t.Fatalf("staged files=%v error=%v", entries, err)
			}
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("unsafe file permissions: %s %v", entry.Name(), err)
				}
			}
			if info, err := os.Stat(res.Directory); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("unsafe directory permissions: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(res.Directory, "release.json"))
			if err != nil || !bytes.Equal(raw, options.Metadata) {
				t.Fatal("exact metadata bytes not preserved")
			}
			sig64, err := os.ReadFile(filepath.Join(res.Directory, "release.sig"))
			if err != nil {
				t.Fatal(err)
			}
			sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig64)))
			if err != nil {
				t.Fatal(err)
			}
			m, err := Verify(raw, sig, options.PublicKey, Policy{Channel: "stable", Installed: "1.2.3", Platform: "linux/amd64", Now: now})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(res.Directory, "artifact"))
			if err != nil || !bytes.Equal(data, payload) || m.VerifyArtifact(bytes.NewReader(data)) != nil {
				t.Fatal("staged artifact did not reverify")
			}
			ready, err := os.ReadFile(filepath.Join(res.Directory, "ready.json"))
			var receipt StageResult
			if err != nil || json.Unmarshal(ready, &receipt) != nil || receipt != res {
				t.Fatalf("completion receipt=%s error=%v", ready, err)
			}
			if _, err := stage(t.Context(), options, transport, clock); err == nil {
				t.Fatal("existing stage overwritten")
			}
			unchanged, _ := os.ReadFile(filepath.Join(res.Directory, "artifact"))
			if !bytes.Equal(unchanged, payload) {
				t.Fatal("repeat stage changed artifact")
			}
		})
	}
	if redirects.Load() != 0 {
		t.Fatal("artifact redirect followed")
	}
	options := stageFixture(t, now, payload)
	options.ArtifactURL = srv.URL + "/artifact"
	if _, err := Stage(t.Context(), options); err == nil {
		t.Fatal("production staging trusted an unprovisioned TLS certificate")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fakeTime.Store(now.UnixNano())
	if _, err := stage(ctx, options, transport, clock); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage: %v", err)
	}
}

type stageTransportFunc func(*http.Request) (*http.Response, error)

func (f stageTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestStageRejectsBeforeNetworkAndPreservesExistingPaths(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("signed bytes")
	transport := stageTransportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid request reached network")
		return nil, fmt.Errorf("unexpected request")
	})
	clock := func() time.Time { return now }
	for _, tc := range []string{"bad-signature", "equal", "older", "channel", "platform", "expired", "http", "credentials", "query", "fragment", "existing-file", "existing-dir", "symlink", "parent-symlink", "public-parent"} {
		t.Run(tc, func(t *testing.T) {
			options := stageFixture(t, now, payload)
			options.ArtifactURL = "https://example.invalid/artifact"
			sentinel := filepath.Join(filepath.Dir(options.Directory), "sentinel")
			if err := os.WriteFile(sentinel, []byte("do not modify"), 0600); err != nil {
				t.Fatal(err)
			}
			switch tc {
			case "bad-signature":
				options.Signature[0] ^= 1
			case "equal":
				options.Policy.Installed = "1.2.4"
			case "older":
				options.Policy.Installed = "2.0.0"
			case "channel":
				options.Policy.Channel = "security"
			case "platform":
				options.Policy.Platform = "linux/arm64"
			case "expired":
				options.Policy.Now = now // live clock, not a supplied stale time
			case "http":
				options.ArtifactURL = "http://example.invalid/artifact"
			case "credentials":
				options.ArtifactURL = "https://user:pass@example.invalid/artifact"
			case "query":
				options.ArtifactURL += "?token=secret"
			case "fragment":
				options.ArtifactURL += "#artifact"
			case "existing-file":
				if err := os.WriteFile(options.Directory, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing-dir":
				if err := os.Mkdir(options.Directory, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(sentinel, options.Directory); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(filepath.Dir(options.Directory), link); err != nil {
					t.Fatal(err)
				}
				options.Directory = filepath.Join(link, "candidate")
			case "public-parent":
				if err := os.Chmod(filepath.Dir(options.Directory), 0755); err != nil {
					t.Fatal(err)
				}
			}
			currentClock := clock
			if tc == "expired" {
				currentClock = func() time.Time { return now.Add(2 * time.Hour) }
			}
			if _, err := stage(t.Context(), options, transport, currentClock); err == nil {
				t.Fatal("unsafe stage accepted")
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "do not modify" {
				t.Fatal("existing sentinel changed")
			}
			if tc == "existing-file" {
				data, err := os.ReadFile(options.Directory)
				if err != nil || string(data) != "existing" {
					t.Fatal("existing target changed")
				}
			}
		})
	}
}

func TestStageDoesNotRemoveForeignCollision(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("signed bytes")
	options := stageFixture(t, now, payload)
	options.ArtifactURL = "https://example.invalid/artifact"
	transport := stageTransportFunc(func(*http.Request) (*http.Response, error) {
		if err := os.WriteFile(filepath.Join(options.Directory, "artifact"), []byte("foreign"), 0600); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})
	if _, err := stage(t.Context(), options, transport, func() time.Time { return now }); err == nil {
		t.Fatal("artifact collision accepted")
	}
	data, err := os.ReadFile(filepath.Join(options.Directory, "artifact"))
	if err != nil || string(data) != "foreign" {
		t.Fatal("cleanup removed a file not created by staging")
	}
	if _, err := os.Stat(filepath.Join(options.Directory, "ready.json")); !os.IsNotExist(err) {
		t.Fatal("failed stage marked ready")
	}
}

type stageEndlessReader struct{ read int }

func (r *stageEndlessReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += len(p)
	return len(p), nil
}

func TestStageStreamingLimitAndSecurityChannel(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("security update artifact")
	options := stageFixture(t, now, payload)
	var meta Metadata
	if err := json.Unmarshal(options.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Channel = "security"
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Metadata, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	options.Signature = ed25519.Sign(key, options.Metadata)
	options.PublicKey, options.Policy.Channel = pub, "security"
	options.ArtifactURL = "https://example.invalid/artifact"
	clock := func() time.Time { return now }
	endless := &stageEndlessReader{}
	transport := stageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: -1, Body: io.NopCloser(endless)}, nil
	})
	if _, err := stage(t.Context(), options, transport, clock); err == nil {
		t.Fatal("unbounded artifact accepted")
	}
	if endless.read != len(payload)+1 {
		t.Fatalf("read beyond signed-size bound: %d bytes", endless.read)
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatal("oversized artifact left staging files")
	}
	transport = stageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})
	res, err := stage(t.Context(), options, transport, clock)
	if err != nil || res.Channel != "security" || !res.ArtifactVerified || res.Installed {
		t.Fatalf("security staging: %+v %v", res, err)
	}
}
