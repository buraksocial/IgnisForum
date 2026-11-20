package release

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPromoteHealthGateAndRollback(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("new executable bytes")
	options := stageFixture(t, now, payload)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(parent, "ignisforum")
	previous := filepath.Join(parent, "ignisforum.previous")
	if err := os.WriteFile(oldPath, []byte("old executable"), 0700); err != nil {
		t.Fatal(err)
	}
	// Use a local transport to avoid network and create a genuine signed stage.
	transport := stageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})
	options.ArtifactURL = "https://example.invalid/artifact"
	result, err := stage(context.Background(), options, transport, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	called := false
	promotion, err := Promote(PromotionOptions{StageDir: result.Directory, Target: oldPath, Previous: previous, PublicKey: options.PublicKey, Policy: options.Policy, Health: func() error { called = true; return errors.New("health") }})
	if err == nil || !strings.Contains(err.Error(), "rolled back") || !called || !promotion.RolledBack {
		t.Fatalf("failed health promotion=%+v err=%v", promotion, err)
	}
	current, _ := os.ReadFile(oldPath)
	if string(current) != "old executable" {
		t.Fatal("health rollback did not restore old binary")
	}
	failed, err := os.ReadFile(oldPath + ".failed")
	if err != nil || string(failed) != string(payload) {
		t.Fatal("failed binary was not quarantined")
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatal("previous path left after rollback")
	}

	// A second stage proves successful promotion and keeps the old binary.
	options2 := stageFixture(t, now, []byte("second executable"))
	options2.ArtifactURL = "https://example.invalid/artifact"
	result2, err := stage(context.Background(), options2, stageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len([]byte("second executable"))), Body: io.NopCloser(strings.NewReader("second executable"))}, nil
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Promote(PromotionOptions{StageDir: result2.Directory, Target: oldPath, Previous: previous, PublicKey: options2.PublicKey, Policy: options2.Policy, Health: func() error { return nil }}); err != nil {
		t.Fatal(err)
	}
	current, _ = os.ReadFile(oldPath)
	if string(current) != "second executable" {
		t.Fatal("successful promotion did not activate binary")
	}
	previousData, _ := os.ReadFile(previous)
	if string(previousData) != "old executable" {
		t.Fatal("successful promotion did not retain previous binary")
	}
}

func TestPromoteRejectsUnsafeOrStaleStages(t *testing.T) {
	now := time.Now().UTC()
	options := stageFixture(t, now, []byte("new"))
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []string{"missing-stage", "wrong-parent", "existing-previous", "stale"} {
		t.Run(tc, func(t *testing.T) {
			stageDir := filepath.Join(t.TempDir(), "stage")
			if tc != "missing-stage" {
				if err := os.Mkdir(stageDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			previous := filepath.Join(parent, "previous")
			switch tc {
			case "wrong-parent":
				previous = filepath.Join(t.TempDir(), "previous")
			case "existing-previous":
				if err := os.WriteFile(previous, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stale":
				options.Policy.Now = now.Add(2 * time.Hour)
			}
			if _, err := Promote(PromotionOptions{StageDir: stageDir, Target: target, Previous: previous, PublicKey: options.PublicKey, Policy: options.Policy}); err == nil {
				t.Fatal("unsafe promotion accepted")
			}
			current, _ := os.ReadFile(target)
			if string(current) != "old" {
				t.Fatal("unsafe promotion changed target")
			}
		})
	}
}
