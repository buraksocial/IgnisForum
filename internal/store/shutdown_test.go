package store

import (
	"fmt"
	"testing"
)

// TestClosePersistsQueuedFlushes exercises shutdown while many small memtables
// are being queued. Close must drain all immutable tables before truncating the
// WAL; reopening must recover every acknowledged write.
func TestClosePersistsQueuedFlushes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 512})
	if err != nil {
		t.Fatal(err)
	}

	const count = 250
	want := make(map[string][]byte, count)
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("shutdown:%04d", i)
		value := []byte(fmt.Sprintf("value-%04d-", i) + fmt.Sprintf("%064d", i))
		if err := s.PutRaw(key, value); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		want[key] = value
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 512})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	for key, value := range want {
		got, ok := s2.GetRaw(key)
		if !ok || string(got) != string(value) {
			t.Errorf("after clean close %s = %q (ok=%v), want %q", key, got, ok, value)
		}
	}
}
