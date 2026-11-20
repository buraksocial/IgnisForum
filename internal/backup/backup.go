// Package backup creates offline, checksummed IgnisDB-Go data backups and
// restores them into a new directory without extracting untrusted paths.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	manifestVersion = 1
	maxDataFileSize = int64(1 << 40)
	maxBackupFiles  = 1_000_000
)

var sstableName = regexp.MustCompile(`^sstable_[0-9]{16}\.db$`)

type Manifest struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	Archive       string    `json:"archive"`
	ArchiveSHA256 string    `json:"archive_sha256"`
	FileCount     int       `json:"file_count"`
}

// Create writes an offline archive and its private JSON manifest. The store
// must be stopped: its lock file is treated as authoritative, and each source
// file is checked for changes while it is copied.
func Create(dataDir, archivePath string) (*Manifest, error) {
	source, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return nil, errors.New("data directory is not a directory")
	}
	if _, err := os.Lstat(filepath.Join(source, ".ignisforum.lock")); err == nil {
		return nil, errors.New("refusing to back up a locked data directory; stop IgnisForum first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect data directory lock: %w", err)
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".ignisforum.lock" {
			return nil, errors.New("data directory became locked during backup preparation")
		}
		if entry.Name() != "wal.log" && !sstableName.MatchString(entry.Name()) {
			return nil, fmt.Errorf("unsupported data directory entry %q; refusing incomplete backup", entry.Name())
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("data directory entry %q is not a regular file", entry.Name())
		}
		files = append(files, entry)
	}
	if len(files) == 0 {
		return nil, errors.New("data directory contains no database files")
	}
	if len(files) > maxBackupFiles {
		return nil, errors.New("data directory has an unreasonable number of files")
	}

	archivePath, err = filepath.Abs(archivePath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(archivePath); err == nil {
		return nil, errors.New("backup archive already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect backup archive path: %w", err)
	}
	manifestPath := archivePath + ".json"
	if _, err := os.Lstat(manifestPath); err == nil {
		return nil, errors.New("backup manifest already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect backup manifest path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(archivePath), ".ignisforum-backup-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return nil, err
	}

	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)
	for _, entry := range files {
		path := filepath.Join(source, entry.Name())
		before, err := os.Stat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() > maxDataFileSize {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("invalid database file %q", entry.Name())
		}
		file, err := os.Open(path)
		if err != nil {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, err
		}
		openedInfo, err := file.Stat()
		if err != nil || !os.SameFile(before, openedInfo) || !openedInfo.Mode().IsRegular() {
			file.Close()
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("database file %q changed during backup preparation", entry.Name())
		}
		header := &tar.Header{Name: entry.Name(), Mode: 0600, Size: before.Size(), ModTime: before.ModTime().UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(header); err != nil {
			file.Close()
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, err
		}
		if _, err := io.CopyN(tw, file, before.Size()); err != nil {
			file.Close()
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("copy database file %q: %w", entry.Name(), err)
		}
		if err := file.Close(); err != nil {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("database file %q changed during backup", entry.Name())
		}
	}
	if err := tw.Close(); err != nil {
		gw.Close()
		tmp.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(source, ".ignisforum.lock")); err == nil {
		return nil, errors.New("data directory became locked during backup")
	}

	digest, err := fileSHA256(tmpName)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, archivePath); err != nil {
		return nil, err
	}
	manifest := &Manifest{Version: manifestVersion, CreatedAt: time.Now().UTC(), Archive: filepath.Base(archivePath), ArchiveSHA256: hex.EncodeToString(digest[:]), FileCount: len(files)}
	if err := writeManifest(manifestPath, manifest); err != nil {
		_ = os.Remove(archivePath)
		return nil, err
	}
	if err := syncDir(filepath.Dir(archivePath)); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Restore verifies the archive and writes only regular database files into a
// fresh destination. Archive paths, links, duplicate entries, and unknown file
// names are rejected before the restored directory is published.
func Restore(archivePath, destination string) (*Manifest, error) {
	manifestPath := archivePath + ".json"
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode backup manifest: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("backup manifest contains trailing data")
	}
	if manifest.Version != manifestVersion || manifest.CreatedAt.IsZero() || manifest.Archive != filepath.Base(archivePath) || manifest.FileCount < 1 || manifest.FileCount > maxBackupFiles || len(manifest.ArchiveSHA256) != sha256.Size*2 {
		return nil, errors.New("invalid backup manifest metadata")
	}
	if _, err := hex.DecodeString(manifest.ArchiveSHA256); err != nil {
		return nil, errors.New("invalid backup manifest checksum")
	}
	digest, err := fileSHA256(archivePath)
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(digest[:]) != manifest.ArchiveSHA256 {
		return nil, errors.New("backup archive SHA-256 mismatch")
	}

	destination, err = filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, errors.New("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(parent, ".ignisforum-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chmod(tmpDir, 0700); err != nil {
		return nil, err
	}

	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return nil, fmt.Errorf("open gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			gz.Close()
			return nil, fmt.Errorf("read backup archive: %w", err)
		}
		name := header.Name
		if name != filepath.Base(name) || name == "." || name == ".." || (name != "wal.log" && !sstableName.MatchString(name)) {
			gz.Close()
			return nil, fmt.Errorf("unsafe or unsupported backup entry %q", name)
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxDataFileSize || seen[name] {
			gz.Close()
			return nil, fmt.Errorf("invalid backup entry %q", name)
		}
		seen[name] = true
		file, err := os.OpenFile(filepath.Join(tmpDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			gz.Close()
			return nil, err
		}
		if _, err := io.CopyN(file, tr, header.Size); err != nil {
			file.Close()
			gz.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			gz.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			gz.Close()
			return nil, err
		}
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if len(seen) != manifest.FileCount || len(seen) > maxBackupFiles || !seen["wal.log"] {
		return nil, errors.New("backup file count or required WAL does not match manifest")
	}
	if err := syncDir(tmpDir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, errors.New("restore destination appeared during restore")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(tmpDir, destination); err != nil {
		return nil, err
	}
	if err := syncDir(parent); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func writeManifest(path string, manifest *Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func fileSHA256(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
