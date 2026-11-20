package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	key, signer, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	const artifact = "staged example"
	base := Metadata{Schema: 1, Product: "IgnisForum", Channel: "security", Version: "1.10.0", Platform: "linux/amd64", Size: int64(len(artifact)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(artifact))), Issued: now.Add(-time.Hour), Expires: now.Add(time.Hour)}
	policy := Policy{Channel: "security", Installed: "1.9.9", Platform: "linux/amd64", Now: now}
	for _, tc := range []struct {
		name    string
		change  func(*Metadata)
		corrupt bool
	}{
		{"valid", nil, false},
		{"modified signature", nil, true},
		{"wrong channel", func(m *Metadata) { m.Channel = "stable" }, false},
		{"wrong platform", func(m *Metadata) { m.Platform = "linux/arm64" }, false},
		{"wrong product", func(m *Metadata) { m.Product = "other" }, false},
		{"expired", func(m *Metadata) { m.Expires = now }, false},
		{"future", func(m *Metadata) { m.Issued = now.Add(time.Minute) }, false},
		{"downgrade", func(m *Metadata) { m.Version = "1.8.11" }, false},
		{"same", func(m *Metadata) { m.Version = policy.Installed }, false},
		{"prerelease", func(m *Metadata) { m.Version = "2.0.0-rc.1" }, false},
		{"oversize", func(m *Metadata) { m.Size = MaxArtifact + 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			if tc.change != nil {
				tc.change(&m)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			sig := ed25519.Sign(signer, raw)
			if tc.corrupt {
				raw = append(raw, ' ')
			}
			verified, err := Verify(raw, sig, key, policy)
			if (err == nil) != (tc.name == "valid") {
				t.Fatalf("verification: %v", err)
			}
			if err != nil {
				return
			}
			if err := verified.VerifyArtifact(strings.NewReader(artifact)); err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{artifact + "!", artifact[:4], "X" + artifact[1:]} {
				if err := verified.VerifyArtifact(strings.NewReader(bad)); err == nil {
					t.Fatal("accepted invalid artifact")
				}
			}
		})
	}
}
