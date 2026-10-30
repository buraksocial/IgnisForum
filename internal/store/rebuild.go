package store

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Derived-index registry.
//
// The engine keeps ZSETs and the FT index in RAM only and exposes no way to
// enumerate or clear them. So the store maintains its own list of index keys it
// has ever created, persisted at meta:index_keys. "Rebuild Content" therefore
// becomes: clear every registered index, then walk the dense primary id spaces
// (1..mark[type]) and re-emit every index member + FT document.
//
// Cost scales with LIVE data, not with the id high-water mark, and needs no key
// scan (§3.1) because ids are dense and marks tell us the upper bound.
const KeyIndexRegistry = "meta:index_keys"

type indexReg struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newIndexReg() *indexReg { return &indexReg{keys: make(map[string]struct{})} }

// Index keys that hold only ids derived from other records (never registered for
// clearing themselves); the registry tracks every index key created via ZAddIdx.
func (r *indexReg) add(key string) {
	r.mu.Lock()
	if _, ok := r.keys[key]; !ok {
		r.keys[key] = struct{}{}
	}
	r.mu.Unlock()
}

func (r *indexReg) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.keys))
	for k := range r.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ZAddIdx is the ONLY sanctioned way to write a derived index member: it registers
// the key (so rebuild can clear it) then delegates to the engine.
func (s *Store) ZAddIdx(indexKey string, score float64, member uint64) int {
	s.reg.add(indexKey)
	return s.ZAdd(indexKey, score, member)
}

// persistRegistry writes the registered index keys.
func (s *Store) persistRegistry() {
	b, err := json.Marshal(s.reg.list())
	if err == nil {
		_ = s.eng.Put([]byte(KeyIndexRegistry), b)
	}
}

// loadRegistry reads back the index key list after a restart.
func (s *Store) loadRegistry() {
	b, ok := s.eng.Get([]byte(KeyIndexRegistry))
	if !ok || len(b) == 0 {
		return
	}
	var keys []string
	if json.Unmarshal(b, &keys) == nil {
		s.reg.mu.Lock()
		for _, k := range keys {
			s.reg.keys[k] = struct{}{}
		}
		s.reg.mu.Unlock()
	}
}

// RebuildFunc re-derives indexes from one primary record. The repo layer registers
// a hook per entity type; the rebuild controller calls them during the walk. This
// keeps the store generic (it does not know about threads) while guaranteeing ONE
// place composes index writes (§5: repo is the single write point).
type RebuildFunc func(entityKey string, blob []byte)

// RebuildHooks are per-entity-type walkers installed by the repo layer.
type RebuildHooks struct {
	mu     sync.RWMutex
	byType map[string]RebuildFunc
}

// NewRebuildHooks returns an empty, ready-to-Set hook set. (Constructing a bare
// &RebuildHooks{} would panic on Set, because byType is a nil map.)
func NewRebuildHooks() *RebuildHooks {
	return &RebuildHooks{byType: make(map[string]RebuildFunc)}
}

func (h *RebuildHooks) Set(typ string, fn RebuildFunc) {
	h.mu.Lock()
	h.byType[typ] = fn
	h.mu.Unlock()
}

func (h *RebuildHooks) get(typ string) (RebuildFunc, bool) {
	h.mu.RLock()
	fn := h.byType[typ]
	h.mu.RUnlock()
	return fn, fn != nil
}

// RebuildReport describes one rebuild pass.
type RebuildReport struct {
	Scanned   int64
	Recovered int64
	Cleared   int64
	Elapsed   float64 // seconds
}

// RebuildContent clears and reconstructs every derived ZSET index and the FT
// index from primary data. It is the guaranteed repair for any index/primary
// divergence (crash, partial write, bug) and runs at startup automatically.
//
// Single writer means this is safe to run while serving traffic; readers may see
// partial indexes until it returns (Rebuilding() is true meanwhile).
func (s *Store) RebuildContent(hooks *RebuildHooks) RebuildReport {
	start := nowSecs()
	s.rebuildRunning.Store(true)
	defer s.rebuildRunning.Store(false)

	rep := RebuildReport{}

	// 1. Clear every registered index (remove all members).
	for _, key := range s.reg.list() {
		members := s.eng.ZRange([]byte(key), 0, -1)
		for _, m := range members {
			s.eng.ZRem([]byte(key), m)
			rep.Cleared++
		}
	}

	// 2. Walk each dense entity space up to its mark, re-emit indexes + FT.
	for _, typ := range walkOrder {
		fn, ok := hooks.get(typ)
		if !ok {
			continue
		}
		max := s.MaxID(typ)
		for id := uint64(1); id <= max; id++ {
			key := typ + ":" + u64str(id)
			blob, ok := s.eng.Get([]byte(key))
			rep.Scanned++
			if !ok || len(blob) == 0 {
				continue // deleted / never existed
			}
			fn(key, blob)
			rep.Recovered++
		}
	}

	// 3. Persist allocator + registry so marks/keys survive next restart.
	s.persistAlloc()
	s.persistRegistry()

	rep.Elapsed = nowSecs() - start
	return rep
}

// walkOrder is dependency-free ordering; posts last so FT (which reads post text)
// runs after thread metadata is available if hooks need it.
var walkOrder = []string{TypeUser, TypeNode, TypeThread, TypePost, TypeReaction, TypeTag}

// nowSecs is a monotonic-ish wall clock in seconds for rebuild timing.
func nowSecs() float64 { return float64(time.Now().UnixNano()) / 1e9 }
