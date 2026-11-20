// ignisforum-update-stage downloads a signed update into a private quarantine.
// It deliberately has no install, restart, unpack, or executable permission path.
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

	"github.com/buraksocial/IgnisForum/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("ignisforum-update-stage", flag.ContinueOnError)
	metadata := flags.String("metadata", "", "local signed release metadata JSON")
	signature := flags.String("signature", "", "local detached base64 Ed25519 signature")
	publicKey := flags.String("public-key", "", "independently provisioned base64 Ed25519 public key")
	channel := flags.String("channel", "", "stable or security")
	installed := flags.String("installed", "", "currently installed MAJOR.MINOR.PATCH version")
	artifactURL := flags.String("artifact-url", "", "operator-selected HTTPS artifact URL (no query, credentials or redirects)")
	directory := flags.String("stage-dir", "", "new quarantine directory under an existing private 0700 parent")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *metadata == "" || *signature == "" || *publicKey == "" || *channel == "" || *installed == "" || *artifactURL == "" || *directory == "" || flags.NArg() != 0 {
		return fmt.Errorf("metadata, signature, public-key, channel, installed, artifact-url and stage-dir are required; positional arguments are not supported")
	}
	raw, err := readLimited(*metadata, release.MaxMetadata)
	if err != nil {
		return err
	}
	sig, err := readBase64(*signature, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	key, err := readBase64(*publicKey, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	result, err := release.Stage(context.Background(), release.StageOptions{
		Metadata: raw, Signature: sig, PublicKey: key,
		Policy:      release.Policy{Channel: *channel, Installed: *installed, Platform: runtime.GOOS + "/" + runtime.GOARCH},
		ArtifactURL: *artifactURL, Directory: *directory,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds size limit: %s", path)
	}
	return data, nil
}

func readBase64(path string, size int) ([]byte, error) {
	data, err := readLimited(path, 256)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("invalid base64 Ed25519 material: %s", path)
	}
	return decoded, nil
}
