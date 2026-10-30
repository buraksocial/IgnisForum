package store

import (
	"testing"
)

// TestRebuildWithHooks exercises the full registry + hook walk: entities are
// written as primaries, index entries are derived via ZAddIdx (which registers
// the keys), the store is closed and reopened (RAM-only indexes vanish), and
// RebuildContent with per-type hooks restores every index + advances marks.
func TestRebuildWithHooks(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	// Fake "thread" entities with a score derived from their blob.
	newHooks := func(st *Store) *RebuildHooks {
		h := NewRebuildHooks()
		h.Set(TypeThread, func(key string, blob []byte) {
			var m map[string]any
			if err := st.DecodeJSON(blob, &m); err != nil {
				return
			}
			st.ZAddIdx("idx:latest_threads", m["score"].(float64), uint64(m["id"].(float64)))
		})
		return h
	}
	hooks := newHooks(s)

	for i := uint64(1); i <= 5; i++ {
		id := s.NextID()
		s.MarkID(TypeThread, id)
		if err := s.PutJSON("thread:"+u64str(id), map[string]any{"id": float64(id), "score": float64(i * 10)}); err != nil {
			t.Fatal(err)
		}
		s.ZAddIdx("idx:latest_threads", float64(i*10), id)
	}
	if got := s.MaxID(TypeThread); got != 5 {
		t.Errorf("MaxID = %d, want 5", got)
	}
	// Registry survives a restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(OpenConfig{DataDir: dir, CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if s2.ZCard("idx:latest_threads") != 0 {
		t.Fatal("index unexpectedly survived restart")
	}

	// The hook writes into the store it is bound to; bind it to the reopened one.
	hooks = newHooks(s2)
	rep := s2.RebuildContent(hooks)
	if rep.Scanned == 0 || rep.Recovered != 5 {
		t.Fatalf("report = %+v, want 5 recovered", rep)
	}
	if s2.ZCard("idx:latest_threads") != 5 {
		t.Errorf("rebuilt card = %d, want 5", s2.ZCard("idx:latest_threads"))
	}
	if s2.Rebuilding() {
		t.Error("Rebuilding flag stuck after rebuild")
	}
	// RebuildContent persisted the registry, so a second pass must clear the
	// known index first (card goes to 0 mid-pass) and end with the same state.
	rep2 := s2.RebuildContent(newHooks(s2))
	if rep2.Recovered != 5 || s2.ZCard("idx:latest_threads") != 5 {
		t.Errorf("second rebuild = %+v, want 5 recovered, card 5", rep2)
	}
	// Marks persisted across the restart (allocator continued from 5).
	if id := s2.NextID(); id != 6 {
		t.Errorf("NextID after restart = %d, want 6", id)
	}
}

// TestZCountUpTo covers the rank-count helper used for deep-link page maths.
func TestZCountUpTo(t *testing.T) {
	s, err := Open(OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i, score := range []float64{10, 20, 30, 40, 50} {
		s.ZAddIdx("idx:t", score, uint64(i+1))
	}
	if got := s.ZCountUpTo("idx:t", 30); got != 3 {
		t.Errorf("ZCountUpTo(30) = %d, want 3", got)
	}
	if got := s.ZCountUpTo("idx:t", 100); got != 5 {
		t.Errorf("ZCountUpTo(100) = %d, want 5", got)
	}
	if got := s.ZCountUpTo("idx:t", 5); got != 0 {
		t.Errorf("ZCountUpTo(5) = %d, want 0", got)
	}
}

// TestDeleteAndRem covers tombstone delete + ZRem (moderation primitives).
func TestDeleteAndRem(t *testing.T) {
	s, err := Open(OpenConfig{DataDir: t.TempDir(), CacheSize: 4 << 20, MemtableSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.PutJSON("post:1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !s.Delete("post:1") {
		t.Error("Delete returned false")
	}
	var v map[string]any
	if ok, _ := s.GetJSON("post:1", &v); ok {
		t.Errorf("post:1 still readable after delete: %v", v)
	}

	s.ZAddIdx("idx:u", 1, 1)
	s.ZAddIdx("idx:u", 2, 2)
	if s.ZRem("idx:u", 1) != 1 {
		t.Error("ZRem did not remove")
	}
	if s.ZCard("idx:u") != 1 {
		t.Errorf("card after ZRem = %d, want 1", s.ZCard("idx:u"))
	}
}
