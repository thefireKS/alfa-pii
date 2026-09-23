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

// BenchmarkGetCreateDuringCleanup measures the latency of Get and Create while
// the store reclaims a large backlog of expired records. It is a diagnostic
// benchmark: it reports the observed read/create latency during cleanup for a
// given sample size and environment, and is not a claim about end-to-end HTTP
// RPS. The backlog is drained in bounded batches, so a single operation should
// not wait for the whole pass.
func BenchmarkGetCreateDuringCleanup(b *testing.B) {
	for _, n := range []int{10_000, 50_000} {
		b.Run(fmt.Sprintf("backlog=%d", n), func(b *testing.B) {
			clock := &fakeClock{t: time.Now()}
			s := NewMemory(Limits{MaxEntries: n + 1, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 20, TTL: time.Minute})
			s.now = clock.now
			for i := 0; i < n; i++ {
				key := fmt.Sprintf("key-%d", i)
				if _, created, err := s.Create(context.Background(), key, build(rec("original", "masked"))); err != nil || !created {
					b.Fatalf("seed create %d: created %v err %v", i, created, err)
				}
			}
			// Expire the whole backlog so the next operation triggers a drain.
			clock.advance(2 * time.Minute)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				key := fmt.Sprintf("live-%d", i)
				start := time.Now()
				_, _, _ = s.Create(context.Background(), key, build(rec("original", "masked")))
				createLatency := time.Since(start)
				start = time.Now()
				_, _ = s.Get(key)
				getLatency := time.Since(start)
				b.ReportMetric(float64(createLatency.Nanoseconds()), "create_ns/op")
				b.ReportMetric(float64(getLatency.Nanoseconds()), "get_ns/op")
			}
		})
	}
}
