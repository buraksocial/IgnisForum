package store

import (
	"strconv"
	"testing"
)

// TestStaleReadAcrossFlush reproduces the engine write-loss: a key is written,
// the memtable is forced to flush (1MB threshold with filler keys), the key is
// rewritten, and the read-back must return the NEW value.
func TestStaleReadAcrossFlush(t *testing.T) {
	s, err := Open(OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Target key, first version.
	if err := s.PutJSON("t:1", map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}

	// Filler until the memtable exceeds 1MB, then flush it synchronously.
	for i := 0; i < 9000; i++ {
		if err := s.PutRaw("filler-"+strconv.Itoa(i), make([]byte, 120)); err != nil {
			t.Fatal(err)
		}
	}
	s.Flush()
	if n := s.Engine().SSTableCount(); n < 1 {
		t.Fatalf("expected a flush to produce an SSTable, got %d", n)
	}

	// Rewrite the target key.
	if err := s.PutJSON("t:1", map[string]any{"v": 2}); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if ok, _ := s.GetJSON("t:1", &got); !ok {
		t.Fatal("t:1 missing after rewrite")
	}
	if got["v"].(float64) != 2 {
		t.Fatalf("stale read after flush: v=%v, want 2 (stats=%v)", got["v"], s.Stats())
	}
}
