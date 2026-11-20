package release

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type StageOptions struct {
	Metadata, Signature []byte
	PublicKey           ed25519.PublicKey
	Policy              Policy
	ArtifactURL         string // operator supplied, never taken from a feed
	Directory           string // must not exist; parent must be private
}

type StageResult struct {
	Version          string `json:"version"`
	Channel          string `json:"channel"`
	Directory        string `json:"directory"`
	ArtifactVerified bool   `json:"artifact_verified"`
	Installed        bool   `json:"installed"` // staging never installs a release
}

// Stage quarantines a verified artifact, without extracting or executing it.
// The caller must independently supply both the public key and artifact URL.
// Nothing in this result authorizes promotion: an installer must reverify the
// signed files and perform its backup, compatibility, health and rollback gates.
func Stage(ctx context.Context, options StageOptions) (StageResult, error) {
	transport := releaseTransport()
	defer transport.CloseIdleConnections()
	return stage(ctx, options, transport, time.Now)
}

func stage(ctx context.Context, options StageOptions, transport http.RoundTripper, now func() time.Time) (result StageResult, err error) {
	policy := options.Policy
	policy.Now = now()
	m, err := Verify(options.Metadata, options.Signature, options.PublicKey, policy)
	if err != nil {
		return result, err
	}
	u, err := url.Parse(options.ArtifactURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return result, errors.New("artifact URL must be HTTPS without credentials, query or fragment")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	directory, err := privateStageDirectory(options.Directory)
	if err != nil {
		return result, err
	}
	files := stagedFiles{directory: directory}
	complete := false
	defer func() {
		if !complete {
			// Only these files belong to this invocation. Never recursively delete
			// a supplied path or remove unrelated files on an interrupted stage.
			for _, name := range files.created {
				_ = os.Remove(filepath.Join(directory, name))
			}
			_ = os.Remove(directory)
		}
	}()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Cache-Control", "no-cache")
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("fetch release artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("artifact returned HTTP %d", response.StatusCode)
	}
	if response.Header.Get("Content-Encoding") != "" || (response.ContentLength >= 0 && response.ContentLength != m.Size) {
		return result, errors.New("unsupported artifact encoding or signed size mismatch")
	}
	file, err := files.open("artifact")
	if err != nil {
		return result, err
	}
	verifyErr := m.VerifyArtifact(io.TeeReader(response.Body, file))
	if verifyErr == nil {
		verifyErr = file.Sync()
	}
	closeErr := file.Close()
	if verifyErr != nil {
		return result, verifyErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	// Expiration can elapse while a bounded but slow transfer is in progress.
	policy.Now = now()
	if _, err := Verify(options.Metadata, options.Signature, options.PublicKey, policy); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := files.write("release.json", options.Metadata); err != nil {
		return result, err
	}
	signature := []byte(base64.StdEncoding.EncodeToString(options.Signature) + "\n")
	if err := files.write("release.sig", signature); err != nil {
		return result, err
	}
	result = StageResult{Version: m.Version, Channel: m.Channel, Directory: directory, ArtifactVerified: true}
	ready, err := json.Marshal(result)
	if err != nil {
		return StageResult{}, err
	}
	// A marker is only a completion receipt, never an authorization to execute.
	if err := files.write("ready.json", ready); err != nil {
		return StageResult{}, err
	}
	if err := syncStageDirectory(directory); err != nil {
		return StageResult{}, err
	}
	if err := syncStageDirectory(filepath.Dir(directory)); err != nil {
		return StageResult{}, err
	}
	complete = true
	return result, nil
}

func privateStageDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("stage directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(absolute)
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if real != parent {
		return "", errors.New("stage parent must not contain symlinks")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("stage parent must be a private directory (0700)")
	}
	// Mkdir refuses existing files, directories and symlinks without replacing
	// them. An incomplete stage is never reused automatically.
	if err := os.Mkdir(absolute, 0700); err != nil {
		return "", err
	}
	return absolute, nil
}

type stagedFiles struct {
	directory string
	created   []string
}

func (s *stagedFiles) open(name string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(s.directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		s.created = append(s.created, name)
	}
	return file, err
}

func (s *stagedFiles) write(name string, data []byte) error {
	file, err := s.open(name)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func syncStageDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
