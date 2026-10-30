package store

import (
	"encoding/binary"
	"encoding/json"
	"strconv"
	"sync/atomic"
)

// Entity type slots. Fixed-size array of atomics: MarkID on the hot path is a
// bounds-checked index + CAS loop with ZERO allocations and no lock, and the
// rebuild walk reads marks without touching any mutex.
const (
	TypeUser     = "user"
	TypeNode     = "node"
	TypeThread   = "thread"
	TypePost     = "post"
	TypeReaction = "reaction_type"
	TypeTag      = "tag"

	numTypes     = 6
	slotUser     = 0
	slotNode     = 1
	slotThread   = 2
	slotPost     = 3
	slotReaction = 4
	slotTag      = 5
)

var typeSlots = map[string]int{
	TypeUser:     slotUser,
	TypeNode:     slotNode,
	TypeThread:   slotThread,
	TypePost:     slotPost,
	TypeReaction: slotReaction,
	TypeTag:      slotTag,
}

// allocRecord is what meta:id_alloc persists: the global high-water mark plus
// per-entity-type high-water marks. The marks let the startup rebuild walk each
// entity space (1..mark[type]) instead of probing every id against every entity
// type — so rebuild cost scales with LIVE data, not with the id high-water mark.
//
// The global counter lives in memory (atomic) for the hot path; this record is
// written at block boundaries and on Close.
type allocRecord struct {
	Alloc uint64            `json:"alloc"`
	Marks map[string]uint64 `json:"marks,omitempty"`
}

// MarkID records that an entity of typ was created with id, if id exceeds the
// current mark. Hot path cost: one atomic load, and only on a genuine new high
// water one CAS. No allocation, no lock, no engine access.
func (s *Store) MarkID(typ string, id uint64) {
	m := s.markSlot(typ)
	if m == nil {
		return
	}
	for {
		cur := m.Load()
		if id <= cur {
			return
		}
		if m.CompareAndSwap(cur, id) {
			return
		}
	}
}

// MaxID returns the known highest id for an entity type (0 if unknown/none).
func (s *Store) MaxID(typ string) uint64 {
	if m := s.markSlot(typ); m != nil {
		return m.Load()
	}
	return 0
}

func (s *Store) markSlot(typ string) *atomic.Uint64 {
	i, ok := typeSlots[typ]
	if !ok {
		return nil
	}
	return &s.marks[i]
}

// marksSnapshot exports the non-zero marks for persistence.
func (s *Store) marksSnapshot() map[string]uint64 {
	out := make(map[string]uint64, numTypes)
	for typ, i := range typeSlots {
		if v := s.marks[i].Load(); v > 0 {
			out[typ] = v
		}
	}
	return out
}

// loadAlloc parses meta:id_alloc in either the current JSON form or the legacy
// 8-byte big-endian form (pre-marks builds).
func (s *Store) loadAlloc(b []byte) {
	if len(b) > 0 && b[0] == '{' {
		var rec allocRecord
		if err := json.Unmarshal(b, &rec); err == nil {
			s.alloc.Store(rec.Alloc)
			for typ, v := range rec.Marks {
				if m := s.markSlot(typ); m != nil && v > m.Load() {
					m.Store(v)
				}
			}
			return
		}
	}
	if len(b) >= 8 {
		s.alloc.Store(binary.BigEndian.Uint64(b))
	}
}

// String helper used by the rebuild walk and repositories.
func u64str(id uint64) string { return strconv.FormatUint(id, 10) }
