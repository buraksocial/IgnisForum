package plugin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadEmptyDirectory(t *testing.T) {
	m, err := Load(context.Background(), filepath.Join(t.TempDir(), "not-installed"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	if got := m.Status(); len(got) != 0 {
		t.Fatalf("plugin status = %#v", got)
	}
	if result, err := m.RunHook(context.Background(), "home.decorate", map[string]int{"count": 1}); err != nil || len(result) != 0 {
		t.Fatalf("empty hook = %#v, %v", result, err)
	}
}

func TestManifestAndDependencyValidation(t *testing.T) {
	valid := Manifest{ID: "sample", Name: "Sample", Version: "1.0.0", APIVersion: APIVersion, Hooks: []string{"home.decorate"}}
	if err := validateManifest("sample", valid); err != nil {
		t.Fatal(err)
	}
	bad := valid
	bad.APIVersion++
	if err := validateManifest("sample", bad); err == nil {
		t.Fatal("unsupported API version accepted")
	}
	bad = valid
	bad.Hooks = []string{"filesystem.read"}
	if err := validateManifest("sample", bad); err == nil {
		t.Fatal("unsupported capability hook accepted")
	}
	manifests := map[string]Manifest{
		"base":  {ID: "base", Version: "2", Name: "Base"},
		"child": {ID: "child", Version: "1", Name: "Child", Depends: []Dependency{{ID: "base", Version: "2"}}},
	}
	order, err := resolveDependencies(manifests)
	if err != nil || !reflect.DeepEqual(order, []string{"base", "child"}) {
		t.Fatalf("dependency order = %#v, %v", order, err)
	}
	manifests["child"] = Manifest{ID: "child", Depends: []Dependency{{ID: "base", Version: "wrong"}}}
	if _, err := resolveDependencies(manifests); err == nil {
		t.Fatal("version mismatch accepted")
	}
	manifests["child"] = Manifest{ID: "child", Depends: []Dependency{{ID: "missing", Version: "1"}}}
	if _, err := resolveDependencies(manifests); err == nil {
		t.Fatal("missing dependency accepted")
	}
	manifests["child"] = Manifest{ID: "child", Depends: []Dependency{{ID: "base", Version: "2"}}}
	manifests["base"] = Manifest{ID: "base", Version: "2", Depends: []Dependency{{ID: "child", Version: "1"}}}
	if _, err := resolveDependencies(manifests); err == nil {
		t.Fatal("dependency cycle accepted")
	}
}

func TestManifestDecoderRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, raw := range []string{
		`{"id":"sample","name":"Sample","version":"1","api_version":1,"hooks":["home.decorate"],"capability":"filesystem"}`,
		`{"id":"sample","name":"Sample","version":"1","api_version":1,"hooks":["home.decorate"]} {}`,
	} {
		root := t.TempDir()
		dir := filepath.Join(root, "sample")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("not wasm"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(context.Background(), root); err == nil {
			t.Fatalf("invalid manifest accepted: %q", raw)
		}
	}
}

func TestLoadRejectsSymlinkPackageFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"sample","name":"Sample","version":"1","api_version":1,"hooks":["home.decorate"]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.wasm")
	if err := os.WriteFile(outside, []byte("not a module"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "plugin.wasm")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), root); err == nil {
		t.Fatal("symlinked plugin module accepted")
	}
}
