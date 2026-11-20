// Package release verifies metadata and locally staged artifacts without installing them.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxMetadata = 16 << 10
const MaxArtifact = 256 << 20

// ErrNoUpdate means the metadata passed every trust and policy check, but its
// version is at or below the installed version. It is not a candidate.
var ErrNoUpdate = errors.New("release is not newer than installed version")

type Metadata struct {
	Schema   int       `json:"schema"`
	Product  string    `json:"product"`
	Channel  string    `json:"channel"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
}

type Policy struct {
	Channel, Installed, Platform string
	Now                          time.Time
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

func version(value string) ([3]int, error) {
	var out [3]int
	m := versionPattern.FindStringSubmatch(value)
	if m == nil {
		return out, fmt.Errorf("invalid stable version %q (expected MAJOR.MINOR.PATCH)", value)
	}
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, nil
}

// Verify checks the detached signature over the exact metadata bytes before parsing.
// It rejects older and equal versions: this verifier is for update candidates only.
func Verify(raw, signature []byte, key ed25519.PublicKey, p Policy) (Metadata, error) {
	var m Metadata
	if len(raw) == 0 || len(raw) > MaxMetadata || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, signature) {
		return m, fmt.Errorf("invalid metadata or release signature")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.Decode(new(any)) != io.EOF {
		return m, fmt.Errorf("invalid release metadata JSON")
	}
	if m.Schema != 1 || m.Product != "IgnisForum" || (p.Channel != "stable" && p.Channel != "security") || m.Channel != p.Channel || p.Platform == "" || m.Platform != p.Platform {
		return m, fmt.Errorf("release schema, product, channel or platform does not match")
	}
	if m.Issued.IsZero() || m.Issued.After(p.Now) || !m.Expires.After(p.Now) || !m.Expires.After(m.Issued) || m.Expires.Sub(m.Issued) > 30*24*time.Hour {
		return m, fmt.Errorf("invalid or expired release time window")
	}
	if m.Size < 1 || m.Size > MaxArtifact || !validHash(m.SHA256) {
		return m, fmt.Errorf("invalid release artifact size or hash")
	}
	installed, err := version(p.Installed)
	if err != nil {
		return m, err
	}
	candidate, err := version(m.Version)
	if err != nil {
		return m, err
	}
	for i := range candidate {
		if candidate[i] > installed[i] {
			return m, nil
		}
		if candidate[i] < installed[i] {
			break
		}
	}
	return m, ErrNoUpdate
}

func validHash(hash string) bool {
	_, err := hex.DecodeString(hash)
	return len(hash) == 2*sha256.Size && hash == strings.ToLower(hash) && err == nil
}

// VerifyArtifact streams at most the signed size plus one byte.
func (m Metadata) VerifyArtifact(r io.Reader) error {
	if m.Size < 1 || m.Size > MaxArtifact || !validHash(m.SHA256) {
		return fmt.Errorf("invalid artifact metadata")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, m.Size+1))
	if err != nil {
		return err
	}
	if n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return fmt.Errorf("artifact size or SHA-256 mismatch")
	}
	return nil
}
