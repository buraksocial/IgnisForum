package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buraksocial/IgnisForum/internal/release"
)

func TestStageCLIRejectsInvalidInputsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("artifact"))
	now := time.Now().UTC()
	meta := release.Metadata{Schema: 1, Product: "IgnisForum", Channel: "stable", Version: "1.2.4", Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Size: 8, SHA256: hex.EncodeToString(hash[:]), Issued: now.Add(-time.Hour), Expires: now.Add(time.Hour)}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"release.json": raw,
		"release.sig":  []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))),
		"key.b64":      []byte(base64.StdEncoding.EncodeToString(pub)),
		"bad.b64":      []byte("invalid material"),
		"large.json":   []byte(strings.Repeat("x", release.MaxMetadata+1)),
	} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := func(name string) string { return filepath.Join(root, name) }
	base := []string{"-metadata", path("release.json"), "-signature", path("release.sig"), "-public-key", path("key.b64"), "-channel", "stable", "-installed", "1.2.3", "-artifact-url", "https://example.invalid/artifact", "-stage-dir", path("candidate")}
	for _, tc := range []struct{ flag, value, want string }{
		{"-public-key", path("bad.b64"), "base64"},
		{"-signature", path("bad.b64"), "base64"},
		{"-metadata", path("large.json"), "size limit"},
		{"-artifact-url", "http://example.invalid/artifact", "HTTPS"},
		{"-installed", "1.2.4", "not newer"},
		{"-channel", "security", "channel"},
		{"-stage-dir", "", "required"},
	} {
		args := append([]string(nil), base...)
		for i := 0; i < len(args); i += 2 {
			if args[i] == tc.flag {
				args[i+1] = tc.value
			}
		}
		var out bytes.Buffer
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q, got %v", tc.flag, tc.want, err)
		}
		if out.Len() != 0 {
			t.Error("rejected command reported a staged artifact")
		}
		if _, err := os.Stat(path("candidate")); !os.IsNotExist(err) {
			t.Fatal("rejected command created output")
		}
	}
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("missing flags accepted")
	}
	if err := run(append(base, "extra"), &bytes.Buffer{}); err == nil {
		t.Fatal("positional arguments accepted")
	}
	if err := run([]string{"-help"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
