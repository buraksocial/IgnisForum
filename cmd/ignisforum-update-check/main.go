package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/buraksocial/IgnisForum/internal/release"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	metadata := flag.String("metadata", "", "path to signed metadata JSON")
	signature := flag.String("signature", "", "path to detached base64 Ed25519 signature")
	pubkey := flag.String("public-key", "", "path to independently provisioned base64 Ed25519 public key")
	channel := flag.String("channel", "", "stable or security")
	installed := flag.String("installed", "", "currently installed MAJOR.MINOR.PATCH version")
	artifact := flag.String("artifact", "", "optional staged artifact path (not installed)")
	flag.Parse()
	if *metadata == "" || *signature == "" || *pubkey == "" || *channel == "" || *installed == "" || flag.NArg() != 0 {
		return fmt.Errorf("metadata, signature, public-key, channel and installed flags are required")
	}
	raw, err := readLimited(*metadata, release.MaxMetadata)
	if err != nil {
		return err
	}
	sig, err := readBase64(*signature, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	key, err := readBase64(*pubkey, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	m, err := release.Verify(raw, sig, key, release.Policy{Channel: *channel, Installed: *installed, Platform: runtime.GOOS + "/" + runtime.GOARCH, Now: time.Now()})
	if err != nil {
		return err
	}
	if *artifact != "" {
		f, err := os.Open(*artifact)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := m.VerifyArtifact(f); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Version          string `json:"version"`
		Channel          string `json:"channel"`
		ArtifactVerified bool   `json:"artifact_verified"`
	}{m.Version, m.Channel, *artifact != ""})
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds limit: %s", path)
	}
	return b, nil
}

func readBase64(path string, size int) ([]byte, error) {
	b, err := readLimited(path, 256)
	if err != nil {
		return nil, err
	}
	value, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(value) != size {
		return nil, fmt.Errorf("invalid base64 Ed25519 material: %s", path)
	}
	return value, nil
}
