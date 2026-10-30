package store

import (
	"fmt"
	"testing"
)

// BenchmarkZAddScaling measures how ZAdd cost grows with index size. The engine's
// sorted-set insert does `append + copy(shift)` on every call, so we expect the
// per-op cost to grow linearly with member count even when appending at the tail.
// This directly bounds how many "latest_posts"-style global indexes we can keep.
func BenchmarkZAddScaling(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 50_000, 100_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s, err := Open(OpenConfig{DataDir: b.TempDir(), CacheSize: 8 << 20, MemtableSize: 256 << 20})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			key := "idx:bench"
			for i := 1; i <= size; i++ {
				s.ZAdd(key, float64(i), uint64(i))
			}
			b.ResetTimer()
			// Keep appending: scores grow, so the insert position is the tail.
			for i := 0; i < b.N; i++ {
				s.ZAdd(key, float64(size+i), uint64(size+i))
			}
		})
	}
}

// BenchmarkReads measures the read hot path at forum-realistic scale, with the
// memtable sized large enough to avoid compaction churn during measurement.
func BenchmarkReads(b *testing.B) {
	s, err := Open(OpenConfig{DataDir: b.TempDir(), CacheSize: 8 << 20, MemtableSize: 256 << 20})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	const N = 20_000
	msg := map[string]any{"thread_id": 1, "user_id": 7, "message_rendered": "konu içi gerçekçi bir mesaj gövdesi 1234567890"}
	for i := 1; i <= N; i++ {
		if err := s.PutJSON(fmt.Sprintf("post:%d", i), msg); err != nil {
			b.Fatal(err)
		}
		s.ZAdd("idx:node_threads:1", float64(i), uint64(i))
	}
	s.Flush()

	b.Run("GetJSON_miss_then_hot", func(b *testing.B) {
		var v map[string]any
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			i := n % N
			_, _ = s.GetJSON(fmt.Sprintf("post:%d", i+1), &v)
		}
	})

	b.Run("ZTop20", func(b *testing.B) {
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			s.ZTop("idx:node_threads:1", 20)
		}
	})

	b.Run("MimicPostWrite", func(b *testing.B) {
		body := map[string]any{"thread_id": 42, "user_id": 7, "message_rendered": "konu içi gerçekçi bir mesaj gövdesi ve biraz daha uzun metin"}
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			id := s.NextID()
			_ = s.PutJSON(fmt.Sprintf("post:%d", id), body)
			s.ZAdd("idx:latest_posts", float64(1_730_000_000+n), id)
			s.ZAdd("idx:thread_posts:42", float64(1_730_000_000+n), id)
			s.ZAdd("idx:user_posts:7", float64(1_730_000_000+n), id)
			s.FTAdd(fmt.Sprintf("post:%d", id), "konu içi gerçekçi mesaj gövdesi uzun metin")
		}
	})
}
