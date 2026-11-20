package plugin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const packageWASM = "0061736d010000000110036000017f60017f017f60027f7f017e0304030001020503010001073804066d656d6f727902001069676e69735f706c7567696e5f61706900000b69676e69735f616c6c6f6300010a69676e69735f686f6f6b00020a1103040041010b050041c0000b040042220b0b28010041000b227b226e6f7469636573223a5b22546573742077696467657420616374697665225d7d"

func writePackage(t *testing.T, root, id, version string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Manifest{ID: id, Name: id, Version: version, APIVersion: APIVersion, Hooks: []string{"home.decorate"}})
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	wasm, err := hex.DecodeString(packageWASM)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func privateDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPackageInstallRollbackAndReversibleRemove(t *testing.T) {
	root := privateDir(t)
	sourceRoot := privateDir(t)
	source := writePackage(t, sourceRoot, "sample", "1.0.0")
	result, err := InstallPackage(context.Background(), root, source)
	if err != nil || result.Version != "1.0.0" || result.Backup != "" {
		t.Fatalf("install=%+v err=%v", result, err)
	}
	if _, err := Load(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	source2Root := privateDir(t)
	source2 := writePackage(t, source2Root, "sample", "2.0.0")
	result, err = InstallPackage(context.Background(), root, source2)
	if err != nil || result.Backup == "" {
		t.Fatalf("upgrade=%+v err=%v", result, err)
	}
	m, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Status()[0].Version; got != "2.0.0" {
		t.Fatalf("active version %s", got)
	}
	_ = m.Close(context.Background())
	rolled, err := RollbackPackage(root, "sample")
	if err != nil || rolled.Version != "1.0.0" {
		t.Fatalf("rollback=%+v err=%v", rolled, err)
	}
	m, err = Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Status()[0].Version; got != "1.0.0" {
		t.Fatalf("rolled version %s", got)
	}
	_ = m.Close(context.Background())
	removed, err := RemovePackage(root, "sample")
	if err != nil || !strings.Contains(removed.Path, ".removed.") {
		t.Fatalf("remove=%+v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "sample")); !os.IsNotExist(err) {
		t.Fatal("removed package still active")
	}
	if _, err := os.Stat(removed.Path); err != nil {
		t.Fatal("quarantine was deleted")
	}
}

func TestPackageTransactionsRejectUnsafeRootsAndCollisions(t *testing.T) {
	sourceRoot := privateDir(t)
	source := writePackage(t, sourceRoot, "sample", "1.0.0")
	publicRoot := t.TempDir()
	if _, err := InstallPackage(context.Background(), publicRoot, source); err == nil {
		t.Fatal("public plugin root accepted")
	}
	root := privateDir(t)
	if _, err := InstallPackage(context.Background(), root, source); err != nil {
		t.Fatal(err)
	}
	source2Root := privateDir(t)
	source2 := writePackage(t, source2Root, "sample", "2.0.0")
	if _, err := InstallPackage(context.Background(), root, source2); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPackage(context.Background(), root, source2); err == nil {
		t.Fatal("occupied rollback slot accepted")
	}
	if _, err := RollbackPackage(root, "missing"); err == nil {
		t.Fatal("missing rollback accepted")
	}
	if _, err := RemovePackage(root, "../sample"); err == nil {
		t.Fatal("path traversal ID accepted")
	}
	if err := os.WriteFile(filepath.Join(source, "extra"), []byte("not copied"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPackage(context.Background(), root, source); err == nil {
		t.Fatal("package with extra file accepted")
	}
}
