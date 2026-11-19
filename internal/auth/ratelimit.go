package auth

import (
	"math"
	"time"

	"github.com/buraksocial/IgnisForum/internal/store"
)

// Rate limiting: ZSET sliding window (§ spec: ZSET tabanlı). Member = a stable
// 8-byte id derived from a bucket seed; score = unix-millis at event time.
// Allow = drop members older than the window, count the rest, add the new one
// if under the limit. The per-(action,bucket) ZSETs stay tiny (window-bounded),
// so the materialising ByScore path is acceptable here (window ≤ 10 min).
type RateLimiter struct {
	s *store.Store
}

func NewRateLimiter(s *store.Store) *RateLimiter {
	return &RateLimiter{s: s}
}

// Allow records one event for (action, bucket) and reports whether the caller
// is under limit within window. retryAfter tells the caller how long until a
// slot frees up (0 when allowed).
func (rl *RateLimiter) Allow(action, bucket string, limit int, window time.Duration) (ok bool, retry time.Duration) {
	key := "idx:ratelimit:" + action + ":" + bucket
	now := time.Now()
	nowMs := float64(now.UnixMilli())
	minScore := float64(now.Add(-window).UnixMilli())

	// Remove expired entries (window is small; ByScore materialise is fine).
	rl.s.ZRemRangeByScore(key, math.Inf(-1), minScore)
	if rl.s.ZCard(key) >= limit {
		// Oldest member decides when a slot frees: it leaves the window at
		// oldest-score + window, so retry after that instant minus now.
		oldest := rl.s.ZOldest(key, 0, 1)
		if len(oldest) > 0 {
			if sc, ok := rl.s.ZScoreOf(key, oldest[0]); ok {
				freeAt := time.UnixMilli(int64(sc)).Add(window)
				if d := time.Until(freeAt); d > 0 {
					return false, d
				}
				return false, time.Second
			}
		}
		return false, window
	}
	// Use the shared atomic allocator for collision-free ZSET members. Gaps in
	// the global ID stream are safe: per-type rebuild marks remain independent.
	rl.s.ZAddIdx(key, nowMs, rl.s.NextID())
	return true, 0
}

// Clear removes all events for (action, bucket) — called after a successful
// login so brute-force counters do not lock out a legitimate user.
func (rl *RateLimiter) Clear(action, bucket string) {
	key := "idx:ratelimit:" + action + ":" + bucket
	rl.s.ZRemRangeByScore(key, math.Inf(-1), math.Inf(1))
}
