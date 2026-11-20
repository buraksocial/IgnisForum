package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buraksocial/IgnisForum/internal/plugin"
)

func TestPluginCLICommandsAndArgumentBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "sample")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(plugin.Manifest{ID: "sample", Name: "Sample", Version: "1.0.0", APIVersion: 1, Hooks: []string{"home.decorate"}})
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	wasm, err := hex.DecodeString("0061736d010000000110036000017f60017f017f60027f7f017e0304030001020503010001073804066d656d6f727902001069676e69735f706c7567696e5f61706900000b69676e69735f616c6c6f6300010a69676e69735f686f6f6b00020a1103040041010b050041c0000b040042220b0b28010041000b227b226e6f7469636573223a5b22546573742077696467657420616374697665225d7d")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.wasm"), wasm, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"install", "-root", root, "-source", source}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"version":"1.0.0"`) {
		t.Fatalf("install output=%s", output.String())
	}
	output.Reset()
	if err := run([]string{"rollback", "-root", root, "-id", "sample"}, &output); err == nil {
		t.Fatal("rollback without backup accepted")
	}
	if err := run([]string{"remove", "-root", root, "-id", "sample", "extra"}, &output); err == nil {
		t.Fatal("extra argument accepted")
	}
	if err := run([]string{"install", "-root", root, "-source", source, "-id", "sample"}, &output); err == nil {
		t.Fatal("install accepted rollback-only id")
	}
	if _, err := plugin.Load(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}
