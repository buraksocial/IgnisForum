package main

import (
	"context"
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
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("ignisforum-update-check", flag.ContinueOnError)
	metadata := flags.String("metadata", "", "path to signed metadata JSON")
	signature := flags.String("signature", "", "path to detached base64 Ed25519 signature")
	pubkey := flags.String("public-key", "", "path to independently provisioned base64 Ed25519 public key")
	channel := flags.String("channel", "", "stable or security")
	installed := flags.String("installed", "", "currently installed MAJOR.MINOR.PATCH version")
	artifact := flags.String("artifact", "", "optional staged artifact path (local mode only)")
	feed := flags.String("feed", "", "operator-configured HTTPS release feed (metadata only)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *pubkey == "" || *channel == "" || *installed == "" || flags.NArg() != 0 {
		return fmt.Errorf("public-key, channel and installed flags are required")
	}
	if (*feed != "" && (*metadata != "" || *signature != "" || *artifact != "")) || (*feed == "" && (*metadata == "" || *signature == "")) {
		return fmt.Errorf("use either -feed alone or local -metadata and -signature (with optional -artifact)")
	}
	key, err := readBase64(*pubkey, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	policy := release.Policy{Channel: *channel, Installed: *installed, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if *feed != "" {
		result, err := release.CheckFeed(context.Background(), *feed, key, policy)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	}
	raw, err := readLimited(*metadata, release.MaxMetadata)
	if err != nil {
		return err
	}
	sig, err := readBase64(*signature, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	policy.Now = time.Now()
	m, err := release.Verify(raw, sig, key, policy)
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
	return json.NewEncoder(out).Encode(struct {
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
