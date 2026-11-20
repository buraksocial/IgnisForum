package release

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOrchestrateRequiresVerifiedBackupBeforePromotion(t *testing.T) {
	var calls []string
	hooks := OrchestrationHooks{
		Stop:         func() error { calls = append(calls, "stop"); return nil },
		Start:        func() error { calls = append(calls, "start"); return nil },
		Backup:       func() error { calls = append(calls, "backup"); return nil },
		VerifyBackup: func() error { calls = append(calls, "verify"); return errors.New("checksum mismatch") },
		Health:       func() error { calls = append(calls, "health"); return nil },
	}
	_, err := Orchestrate(PromotionOptions{}, hooks)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err=%v", err)
	}
	want := []string{"stop", "backup", "verify", "stop", "start"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestOrchestrateRejectsDirectHealthAndIncompleteHooks(t *testing.T) {
	hooks := OrchestrationHooks{Stop: func() error { return nil }}
	if _, err := Orchestrate(PromotionOptions{Health: func() error { return nil }}, hooks); err == nil {
		t.Fatal("direct health accepted")
	}
	if _, err := Orchestrate(PromotionOptions{}, hooks); err == nil {
		t.Fatal("incomplete hooks accepted")
	}
}
