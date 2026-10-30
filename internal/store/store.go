// Package store is the single wrapper around the embedded IgnisDB-Go engine.
//
// It enforces the non-negotiable database rules (architecture §3):
//
//  1. No key scans exist in the engine, so every listing is served from a derived
//     ZSET index. Prefix-scan code is never written here.
//  2. There is no INCR: IDs come from an atomic.Uint64 seeded from meta:id_alloc
//     and persisted every idBlockSize allocations and on Close().
//  3. Entities are JSON blobs; updates are read-modify-write (cheap via the hot cache).
//  4. ZSETs and the FT index live only in RAM and are NOT in the WAL. They are
//     rebuilt from primary data on startup (and on demand via RebuildContent).
//     IDs are dense (1..N), so enumeration is a bounded id loop, not a scan.
//  5. Single writer: a meta:lock (PID + mtime, heartbeat) guards the data-dir.
//  6. No atomic multi-key transactions: write ORDER is primary FIRST, then indexes.
//     A crash leaves an index short, which RebuildContent repairs; primary never
//     half-writes (WAL CRC32).
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buraksocial/IgnisDB-Go/pkg/engine"
)

// meta:* and reserved keys.
const (
	KeyIDAlloc   = "meta:id_alloc"
	KeyLock      = "meta:lock"
	KeyVersion   = "meta:version"
	KeyForumTree = "forum_tree" // primary node hierarchy blob

	// idBlockSize is how many IDs we hand out before re-persisting meta:id_alloc.
	// 1000 keeps the worst-case ID reuse gap after a crash to 1000 while making
	// ID allocation essentially free (a single atomic increment) on the hot path.
	idBlockSize = 1000
)

// ErrNotOpen is returned when operations are attempted on a closed store.
var ErrNotOpen = errors.New("store: not open")

// Store wraps the LSM engine and owns the derived-index rebuild discipline.
type Store struct {
	eng *engine.Engine

	// ID allocator: seeded from meta:id_alloc on Open; the *reserved* next id.
	alloc     atomic.Uint64
	persisted atomic.Uint64 // last value written to meta:id_alloc

	// per-entity high-water marks (see ids.go): enables O(live) dense rebuild
	// walks instead of probing every id against every entity type.
	marks [numTypes]atomic.Uint64

	// registry of derived index keys we created (so Rebuild can clear them).
	reg *indexReg

	// rebuildRunning flags that derived indexes are still being rebuilt from
	// primary; listings during this window may be partial.
	rebuildRunning atomic.Bool

	closed atomic.Bool

	dataDir string
}

// OpenConfig is the subset of configuration the store needs.
type OpenConfig struct {
	DataDir             string
	MemtableSize        int64
	CacheSize           int
	CompactionThreshold int
}

// Open acquires the single-instance lock, starts the engine, loads the ID
// allocator, and verifies schema version.
func Open(cfg OpenConfig) (*Store, error) {
	if err := acquireLock(cfg.DataDir); err != nil {
		return nil, err
	}

	eng, err := engine.Open(engine.Config{
		DataDir:             cfg.DataDir,
		MemtableSize:        cfg.MemtableSize,
		CacheSize:           cfg.CacheSize,
		CompactionThreshold: cfg.CompactionThreshold,
	})
	if err != nil {
		releaseLock(cfg.DataDir)
		return nil, fmt.Errorf("engine open: %w", err)
	}

	s := &Store{eng: eng, dataDir: cfg.DataDir, reg: newIndexReg()}

	// Seed the allocator + per-type marks from the persisted high-water record.
	if b, ok := eng.Get([]byte(KeyIDAlloc)); ok && len(b) > 0 {
		s.loadAlloc(b)
	}
	s.persisted.Store(s.alloc.Load())
	s.loadRegistry()

	return s, nil
}

// NextID returns a new globally unique, dense id (1-based). It does NOT touch the
// engine unless the block boundary is crossed, so the write hot path pays only an
// atomic increment (per §3.2).
func (s *Store) NextID() uint64 {
	id := s.alloc.Add(1)
	// Persist a block ahead so a crash cannot reuse ids below the persisted mark.
	if id-s.persisted.Load() >= idBlockSize {
		s.persistAlloc()
	}
	return id
}

// persistAlloc writes the allocator record (global mark + per-type marks).
func (s *Store) persistAlloc() {
	v := s.alloc.Load()
	b, err := json.Marshal(allocRecord{Alloc: v, Marks: s.marksSnapshot()})
	// Best-effort: WAL is durable; if Put fails we simply reuse fewer ids.
	if err == nil && s.eng.Put([]byte(KeyIDAlloc), b) == nil {
		s.persisted.Store(v)
	}
}

// --- JSON entity helpers (read-modify-write model, §3.3) ---

// PutJSON marshals v and writes it under key.
func (s *Store) PutJSON(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.eng.Put([]byte(key), b)
}

// GetJSON fetches key and unmarshals into v. Returns (false, nil) on miss.
func (s *Store) GetJSON(key string, v any) (bool, error) {
	b, ok := s.eng.Get([]byte(key))
	if !ok || len(b) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, err
	}
	return true, nil
}

// DecodeJSON decodes a raw value into v (pair with GetRaw when you already hold
// the bytes, e.g. after a single Get of a blob used by several handlers).
// (Named DecodeJSON, not UnmarshalJSON, to avoid clashing with the
// json.Unmarshaler interface signature.)
func (s *Store) DecodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }

// PutRaw / GetRaw / DeleteRaw are thin passthroughs for non-JSON blobs.
func (s *Store) PutRaw(key string, val []byte) error { return s.eng.Put([]byte(key), val) }
func (s *Store) GetRaw(key string) ([]byte, bool)    { return s.eng.Get([]byte(key)) }
func (s *Store) Delete(key string) bool              { return s.eng.Delete([]byte(key)) }

// --- Derived ZSET index helpers (score is float64; members are id bytes) ---

// ZAdd indexes member under a sorted-set key with the given score.
func (s *Store) ZAdd(indexKey string, score float64, member uint64) int {
	return s.eng.ZAdd([]byte(indexKey), score, u64b(member))
}

// ZRem removes a member from a sorted-set index.
func (s *Store) ZRem(indexKey string, member uint64) int {
	return s.eng.ZRem([]byte(indexKey), u64b(member))
}

// ZCard reports the number of members in an index.
func (s *Store) ZCard(indexKey string) int { return s.eng.ZCard([]byte(indexKey)) }

// ZTop returns up to n member ids with the HIGHEST scores (newest / most likes /
// most views), ordered descending. This is THE forum listing primitive.
//
// Implementation note (perf-critical): we deliberately do NOT use
// engine.ZRevRangeByScore here. This engine materialises the entire score range
// before applying offset/limit (it calls ZRangeByScore with limit=-1, i.e. no
// limit, then reverses and slices). For a "top 20" out of 100k members that is
// O(n) — ~9ms and ~2MB of garbage per listing, which breaks §10 latency budgets.
//
// Instead we compute the rank window from ZCard (O(1)) and read exactly n members
// with the ascending ZRange(start,stop), which is O(k). We then reverse those k
// members ourselves. Equal scores keep the engine's canonical (member-ascending)
// ordering.
func (s *Store) ZTop(indexKey string, n int) []uint64 {
	return s.ZPage(indexKey, 0, n)
}

// ZPage returns n member ids starting at `offset`, highest-score first. Paginated
// form of ZTop and the basis of every "next page" in the forum. O(n), not O(size).
func (s *Store) ZPage(indexKey string, offset, n int) []uint64 {
	if n <= 0 {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	card := s.eng.ZCard([]byte(indexKey))
	if card == 0 || offset >= card {
		return nil
	}
	if n > card-offset {
		n = card - offset
	}
	// Descending rank r ↔ ascending index (card-1-r). Descending ranks
	// [offset, offset+n) ↔ ascending slice [card-offset-n, card-offset-1].
	start := card - offset - n
	stop := card - offset - 1
	raw := s.eng.ZRange([]byte(indexKey), start, stop) // ascending, O(n)
	out := make([]uint64, len(raw))
	for i, m := range raw { // fill reversed: highest score first
		if len(m) == 8 {
			out[len(raw)-1-i] = binary.BigEndian.Uint64(m)
		}
	}
	return out
}

// ZOldest returns up to n member ids with the LOWEST scores, ascending — used for
// "first posts"/"oldest threads" style orderings. O(n).
func (s *Store) ZOldest(indexKey string, offset, n int) []uint64 {
	if n <= 0 {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	raw := s.eng.ZRange([]byte(indexKey), offset, offset+n-1)
	out := make([]uint64, 0, len(raw))
	for _, m := range raw {
		if len(m) == 8 {
			out = append(out, binary.BigEndian.Uint64(m))
		}
	}
	return out
}

// ZScoreOf returns a single member's score (e.g. a member's reaction rank).
func (s *Store) ZScoreOf(indexKey string, member uint64) (float64, bool) {
	return s.eng.ZScore([]byte(indexKey), u64b(member))
}

// ZCountUpTo counts the members with score <= maxScore, i.e. the 1-based rank
// ceiling of a member by score. The engine's ByScore path materialises the whole
// range before counting, so this is O(range size), NOT O(1): call it only on
// small sets (per-thread post indexes) — e.g. deep-link page resolution. It must
// never be used as a listing primitive (use ZPage).
func (s *Store) ZCountUpTo(indexKey string, maxScore float64) int {
	return len(s.eng.ZRangeByScore([]byte(indexKey), math.Inf(-1), maxScore, 0, -1))
}

// FTAdd / FTSearch / FTRemove are exposed for full-text indexing (docID = "post:{id}").
func (s *Store) FTAdd(docID, text string) { s.eng.FTAdd(docID, text) }
func (s *Store) FTSearch(q string, limit int) []FTHit {
	res := s.eng.FTSearch(q, limit)
	out := make([]FTHit, 0, len(res))
	for _, r := range res {
		out = append(out, FTHit{DocID: r.DocID, Score: r.Score})
	}
	return out
}
func (s *Store) FTRemove(docID string) { s.eng.FTRemove(docID) }

// FTHit is the store-layer search result.
type FTHit struct {
	DocID string
	Score float64
}

// Rebuilding reports whether derived indexes are still being reconstructed from
// primary data. Handlers can use this to add a "content is being rebuilt" banner.
func (s *Store) Rebuilding() bool { return s.rebuildRunning.Load() }

// SetRebuilding toggles the rebuild flag (the rebuild controller owns this).
func (s *Store) SetRebuilding(v bool) { s.rebuildRunning.Store(v) }

// Stats returns engine statistics for the healthz endpoint and admin Server Status.
func (s *Store) Stats() map[string]interface{} { return s.eng.GetStats() }

// Flush forces the active memtable to an SSTable (before backup, on shutdown, etc.).
func (s *Store) Flush() { s.eng.Flush() }

// Engine returns the raw engine handle. New code should prefer store methods so
// the index/write-order discipline stays in one place.
func (s *Store) Engine() *engine.Engine { return s.eng }

// Close persists the allocator and closes the engine + lock.
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.persistAlloc()
	s.persistRegistry() // so the next restart's RebuildContent can clear these keys
	err := s.eng.Close()
	releaseLock(s.dataDir)
	return err
}

// u64b encodes an id as 8-byte big-endian. Big-endian keeps ZSET member byte
// ordering aligned with numeric ordering, which is handy for tie-breaking.
func u64b(id uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], id)
	return b[:]
}

// --- single-instance lock (§3.5) ---

// acquireLock writes meta:lock with the current PID and a heartbeat mtime. If a
// live instance already holds it (fresh heartbeat), it refuses to open the dir.
func acquireLock(dir string) error {
	lockPath := dir + "/.ignisforum.lock"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if b, err := os.ReadFile(lockPath); err == nil {
		var pid int
		var beat int64
		if n, _ := fmt.Sscanf(string(b), "%d %d", &pid, &beat); n == 2 {
			if time.Now().Unix()-beat < 10 && pidAlive(pid) {
				return fmt.Errorf("store: data-dir %q already locked by live pid %d", dir, pid)
			}
		}
	}
	return writeLock(lockPath)
}

// StartHeartbeat refreshes the lock file every 5s while the store is open.
// Stop it via the returned cancel func.
func StartHeartbeat(dir string) (cancel func()) {
	lockPath := dir + "/.ignisforum.lock"
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = writeLock(lockPath)
			}
		}
	}()
	return func() { close(done) }
}

func writeLock(path string) error {
	b := []byte(fmt.Sprintf("%d %d", os.Getpid(), time.Now().Unix()))
	return os.WriteFile(path, b, 0o644)
}

func releaseLock(dir string) {
	_ = os.Remove(dir + "/.ignisforum.lock")
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Linux FindProcess always succeeds; Signal(syscall.Signal(0)) tests existence.
	return p.Signal(syscall.Signal(0)) == nil
}
