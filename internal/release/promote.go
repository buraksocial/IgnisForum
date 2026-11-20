package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type PromotionOptions struct {
	StageDir, Target, Previous string
	PublicKey                  ed25519.PublicKey
	Policy                     Policy
	// Health runs only after the new executable is atomically active. Returning
	// an error triggers a best-effort binary rollback before Promote returns.
	Health func() error
}

type PromotionResult struct {
	Version    string `json:"version"`
	Target     string `json:"target"`
	Previous   string `json:"previous"`
	RolledBack bool   `json:"rolled_back"`
}

// Promote verifies the complete staged receipt again, atomically replaces one
// executable, and runs the caller's health gate. It never starts a process or
// stops a service; the caller owns service orchestration and database freeze.
func Promote(options PromotionOptions) (result PromotionResult, err error) {
	if options.StageDir == "" || options.Target == "" || options.Previous == "" || len(options.PublicKey) != ed25519.PublicKeySize {
		return result, errors.New("stage directory, target, previous and public key are required")
	}
	metadata, err := readPrivateFile(filepath.Join(options.StageDir, "release.json"), MaxMetadata)
	if err != nil {
		return result, err
	}
	signatureText, err := readPrivateFile(filepath.Join(options.StageDir, "release.sig"), 256)
	if err != nil {
		return result, err
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(string(trimSpace(signatureText)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return result, errors.New("invalid staged release signature")
	}
	options.Policy.Now = time.Now().UTC()
	metadataInfo, err := Verify(metadata, signature, options.PublicKey, options.Policy)
	if err != nil {
		return result, err
	}
	artifactPath := filepath.Join(options.StageDir, "artifact")
	artifact, err := os.Open(artifactPath)
	if err != nil {
		return result, err
	}
	if err = metadataInfo.VerifyArtifact(artifact); err == nil {
		err = artifact.Close()
	} else {
		_ = artifact.Close()
	}
	if err != nil {
		return result, err
	}
	var receipt StageResult
	receiptData, err := readPrivateFile(filepath.Join(options.StageDir, "ready.json"), 4096)
	if err != nil || json.Unmarshal(receiptData, &receipt) != nil || receipt.Version != metadataInfo.Version || !receipt.ArtifactVerified || receipt.Installed {
		return result, errors.New("staged completion receipt is invalid")
	}
	parent := filepath.Dir(options.Target)
	if err := privateParent(parent); err != nil {
		return result, err
	}
	if filepath.Dir(options.Previous) != parent {
		return result, errors.New("previous binary must share target directory")
	}
	if _, err := os.Lstat(options.Previous); err == nil {
		return result, errors.New("previous binary path already exists")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if info, err := os.Lstat(options.Target); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return result, errors.New("target must be an existing regular non-symlink file")
	}
	tmp, err := os.OpenFile(filepath.Join(parent, ".ignisforum-promote-"+metadataInfo.Version), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return result, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	copyFile, copyErr := os.Open(artifactPath)
	if copyErr == nil {
		_, copyErr = io.Copy(tmp, copyFile)
		_ = copyFile.Close()
	}
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return result, copyErr
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return result, err
	}
	if err := os.Rename(options.Target, options.Previous); err != nil {
		return result, err
	}
	if err := os.Rename(tmpPath, options.Target); err != nil {
		_ = os.Rename(options.Previous, options.Target)
		return result, err
	}
	result = PromotionResult{Version: metadataInfo.Version, Target: options.Target, Previous: options.Previous}
	if options.Health != nil {
		if healthErr := options.Health(); healthErr != nil {
			result.RolledBack = true
			if rollbackErr := rollbackBinary(options.Target, options.Previous); rollbackErr != nil {
				return result, fmt.Errorf("health failed: %v; rollback failed: %w", healthErr, rollbackErr)
			}
			return result, fmt.Errorf("health check failed; binary rolled back: %w", healthErr)
		}
	}
	return result, nil
}

func rollbackBinary(target, previous string) error {
	failed := target + ".failed"
	if _, err := os.Lstat(failed); err == nil {
		return errors.New("failed binary quarantine already exists")
	}
	if err := os.Rename(target, failed); err != nil {
		return err
	}
	if err := os.Rename(previous, target); err != nil {
		_ = os.Rename(failed, target)
		return err
	}
	return nil
}

func readPrivateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("invalid staged file")
	}
	return os.ReadFile(path)
}

func privateParent(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("promotion parent must be a private real directory")
	}
	return nil
}

func trimSpace(data []byte) []byte {
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\n' || data[0] == '\r' || data[0] == '\t') {
		data = data[1:]
	}
	for len(data) > 0 && (data[len(data)-1] == ' ' || data[len(data)-1] == '\n' || data[len(data)-1] == '\r' || data[len(data)-1] == '\t') {
		data = data[:len(data)-1]
	}
	return data
}
