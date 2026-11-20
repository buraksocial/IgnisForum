package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buraksocial/IgnisForum/internal/store"
)

func TestBackupRestoreReopensDatabaseAndRebuildsFromPrimary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	s, err := store.Open(store.OpenConfig{DataDir: source, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutJSON("user:42", map[string]any{"id": 42, "username": "backup-user"}); err != nil {
		t.Fatal(err)
	}
	s.MarkID(store.TypeUser, 42)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, "backup", "forum.tar.gz")
	manifest, err := Create(source, archive)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FileCount < 2 {
		t.Fatalf("expected WAL and database files in manifest: %+v", manifest)
	}
	for _, name := range []string{archive, archive + ".json"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s permissions = %o, want 0600", name, info.Mode().Perm())
		}
	}

	destination := filepath.Join(root, "restored")
	if _, err := Restore(archive, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(store.OpenConfig{DataDir: destination, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	var user struct {
		ID       uint64 `json:"id"`
		Username string `json:"username"`
	}
	if ok, err := restored.GetJSON("user:42", &user); err != nil || !ok || user.ID != 42 || user.Username != "backup-user" {
		t.Fatalf("restored primary: %+v found=%v err=%v", user, ok, err)
	}
	if restored.MaxID(store.TypeUser) != 42 {
		t.Fatalf("restored ID high-water mark = %d", restored.MaxID(store.TypeUser))
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRefusesLockedOrUnknownDataDirectory(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "wal.log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, ".ignisforum.lock"), []byte("123 456"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(data, filepath.Join(root, "locked.tar.gz")); err == nil {
		t.Fatal("accepted a locked data directory")
	}
	if err := os.Remove(filepath.Join(data, ".ignisforum.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "unexpected.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(data, filepath.Join(root, "unknown.tar.gz")); err == nil {
		t.Fatal("accepted an unrecognized data file")
	}
}

func TestRestoreRejectsTamperedArchiveAndExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	s, err := store.Open(store.OpenConfig{DataDir: source, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutJSON("node:1", map[string]any{"id": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "forum.tar.gz")
	if _, err := Create(source, archive); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("tamper")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "bad-restore")
	if _, err := Restore(archive, destination); err == nil {
		t.Fatal("accepted a checksum-mismatched archive")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed restore left destination behind: %v", err)
	}
}

func TestRestoreRejectsTraversalEntry(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "traversal.tar.gz")
	f, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(archivePath+".json", &Manifest{Version: manifestVersion, CreatedAt: time.Now().UTC(), Archive: filepath.Base(archivePath), ArchiveSHA256: hex.EncodeToString(digest[:]), FileCount: 1}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restore")
	if _, err := Restore(archivePath, destination); err == nil {
		t.Fatal("accepted a traversal path in the archive")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside destination: %v", err)
	}
}
