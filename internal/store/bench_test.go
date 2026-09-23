package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkGetLiveRecords measures the cost of a Get on a store holding a given
// number of live records. It is a diagnostic benchmark: it shows how the read
// cost scales with the number of stored correspondences, not a proof of the
// service's end-to-end RPS.
func BenchmarkGetLiveRecords(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			s := NewMemory(Limits{MaxEntries: n + 1, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 20, TTL: time.Hour})
			for i := 0; i < n; i++ {
				key := fmt.Sprintf("key-%d", i)
				if _, created, err := s.Create(context.Background(), key, build(rec("original", "masked"))); err != nil || !created {
					b.Fatalf("seed create %d: created %v err %v", i, created, err)
				}
			}
			// Read a key that is present so the benchmark measures the live
			// lookup path, not a miss.
			key := fmt.Sprintf("key-%d", n/2)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := s.Get(key); !ok {
					b.Fatal("expected record present")
				}
			}
		})
	}
}
