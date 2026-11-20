package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PackageResult describes an operator-side filesystem transaction. The web
// process never calls these functions and never receives package bytes.
type PackageResult struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Backup  string `json:"backup,omitempty"`
}

// InstallPackage validates a package in isolation, then places exactly its
// manifest and WASM module under root with a rename-based cutover. Existing
// packages are moved to one rollback slot and are never overwritten.
func InstallPackage(ctx context.Context, root, source string) (PackageResult, error) {
	manifest, err := readPackageManifest(source)
	if err != nil {
		return PackageResult{}, err
	}
	if err := validatePackageLayout(source); err != nil {
		return PackageResult{}, err
	}
	if err := ensurePrivateRoot(root); err != nil {
		return PackageResult{}, err
	}
	testRoot, err := os.MkdirTemp("", "ignisforum-plugin-validate-")
	if err != nil {
		return PackageResult{}, err
	}
	defer os.RemoveAll(testRoot)
	if err := copyPackage(source, filepath.Join(testRoot, manifest.ID)); err != nil {
		return PackageResult{}, err
	}
	if err := validatePackageInIsolation(ctx, testRoot); err != nil {
		return PackageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PackageResult{}, err
	}
	transaction, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return PackageResult{}, err
	}
	defer os.RemoveAll(transaction)
	packagePath := filepath.Join(transaction, manifest.ID)
	if err := copyPackage(source, packagePath); err != nil {
		return PackageResult{}, err
	}
	target := filepath.Join(root, manifest.ID)
	backup := filepath.Join(root, manifest.ID+".rollback")
	if _, err := os.Lstat(backup); err == nil {
		return PackageResult{}, errors.New("rollback slot already exists; review it before installing")
	} else if !os.IsNotExist(err) {
		return PackageResult{}, err
	}
	hadTarget := false
	if _, err := os.Lstat(target); err == nil {
		info, statErr := os.Lstat(target)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return PackageResult{}, errors.New("active package must be a real directory")
		}
		hadTarget = true
		if err := os.Rename(target, backup); err != nil {
			return PackageResult{}, fmt.Errorf("preserve current package: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return PackageResult{}, err
	}
	if err := os.Rename(packagePath, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return PackageResult{}, fmt.Errorf("activate package: %w", err)
	}
	return PackageResult{ID: manifest.ID, Version: manifest.Version, Path: target, Backup: backupIf(hadTarget, backup)}, nil
}

// RollbackPackage restores the previous package from its rollback slot. The
// current package is preserved in a second slot only when that slot is absent.
func RollbackPackage(root, id string) (PackageResult, error) {
	if !idPattern.MatchString(id) {
		return PackageResult{}, errors.New("invalid plugin ID")
	}
	if err := ensurePrivateRoot(root); err != nil {
		return PackageResult{}, err
	}
	target, backup := filepath.Join(root, id), filepath.Join(root, id+".rollback")
	if _, err := os.Lstat(backup); err != nil {
		return PackageResult{}, errors.New("no rollback package is available")
	}
	if err := validatePackageLayout(backup); err != nil {
		return PackageResult{}, fmt.Errorf("rollback package: %w", err)
	}
	validationRoot, err := os.MkdirTemp("", "ignisforum-plugin-rollback-")
	if err != nil {
		return PackageResult{}, err
	}
	defer os.RemoveAll(validationRoot)
	if err := copyPackage(backup, filepath.Join(validationRoot, id)); err != nil {
		return PackageResult{}, err
	}
	if err := validatePackageInIsolation(context.Background(), validationRoot); err != nil {
		return PackageResult{}, err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return PackageResult{}, errors.New("active package must be a real directory")
	}
	if _, err := os.Lstat(filepath.Join(root, id+".rollback.current")); err == nil {
		return PackageResult{}, errors.New("current rollback slot already exists")
	}
	if err := os.Rename(target, filepath.Join(root, id+".rollback.current")); err != nil {
		return PackageResult{}, err
	}
	if err := os.Rename(backup, target); err != nil {
		_ = os.Rename(filepath.Join(root, id+".rollback.current"), target)
		return PackageResult{}, err
	}
	manifest, err := readPackageManifest(target)
	if err != nil {
		return PackageResult{}, err
	}
	return PackageResult{ID: manifest.ID, Version: manifest.Version, Path: target, Backup: filepath.Join(root, id+".rollback.current")}, nil
}

// RemovePackage is reversible: it moves the package to a timestamped quarantine
// instead of deleting bytes. The caller must explicitly clean that quarantine.
func RemovePackage(root, id string) (PackageResult, error) {
	if !idPattern.MatchString(id) {
		return PackageResult{}, errors.New("invalid plugin ID")
	}
	if err := ensurePrivateRoot(root); err != nil {
		return PackageResult{}, err
	}
	target := filepath.Join(root, id)
	manifest, err := readPackageManifest(target)
	if err != nil {
		return PackageResult{}, err
	}
	quarantine := filepath.Join(root, fmt.Sprintf("%s.removed.%d", id, time.Now().UnixNano()))
	if err := os.Rename(target, quarantine); err != nil {
		return PackageResult{}, err
	}
	return PackageResult{ID: id, Version: manifest.Version, Path: quarantine}, nil
}

func readPackageManifest(dir string) (Manifest, error) {
	data, err := readRegular(filepath.Join(dir, "plugin.json"), maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, errors.New("manifest has trailing JSON")
	}
	if err := validateManifest(filepath.Base(dir), manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validatePackageLayout(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("package must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 2 {
		return errors.New("package must contain only plugin.json and plugin.wasm")
	}
	for _, entry := range entries {
		if entry.Name() != "plugin.json" && entry.Name() != "plugin.wasm" {
			return fmt.Errorf("unexpected package file %q", entry.Name())
		}
	}
	for _, name := range []string{"plugin.json", "plugin.wasm"} {
		if _, err := readRegular(filepath.Join(dir, name), maxManifestBytes+maxModuleBytes); err != nil {
			return err
		}
	}
	return nil
}

func ensurePrivateRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("plugin root must be a private real directory (0700)")
	}
	return nil
}

func copyPackage(source, target string) error {
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	for _, name := range []string{"plugin.json", "plugin.wasm"} {
		in, err := os.Open(filepath.Join(source, name))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(target, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = io.Copy(out, in)
		}
		if closeErr := in.Close(); err == nil {
			err = closeErr
		}
		if closeErr := out.Close(); err == nil && out != nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validatePackageInIsolation(ctx context.Context, root string) error {
	probe, err := Load(ctx, root)
	if err != nil {
		return fmt.Errorf("package validation: %w", err)
	}
	_ = probe.Close(ctx)
	return nil
}

func backupIf(ok bool, path string) string {
	if ok {
		return path
	}
	return ""
}
