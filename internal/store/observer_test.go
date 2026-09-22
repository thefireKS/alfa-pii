package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingObserver captures store lifecycle events for assertions.
type recordingObserver struct {
	mu        sync.Mutex
	added     int
	removed   int
	bytes     int64
	ttl       int
	failures  map[string]int
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{failures: make(map[string]int)}
}

func (o *recordingObserver) RecordAdded() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.added++
}
func (o *recordingObserver) RecordRemoved() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removed++
}
func (o *recordingObserver) BytesDelta(d int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bytes += d
}
func (o *recordingObserver) TTLExpired() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ttl++
}
func (o *recordingObserver) Failure(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures[reason]++
}

func (o *recordingObserver) snapshot() (added, removed, ttl int, bytes int64, failures map[string]int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := make(map[string]int, len(o.failures))
	for k, v := range o.failures {
		f[k] = v
	}
	return o.added, o.removed, o.ttl, o.bytes, f
}

// TestObserverRecordsCreationAndEviction verifies the observer is notified when
// a record is added and when its TTL expires.
func TestObserverRecordsCreationAndEviction(t *testing.T) {
	obs := newRecordingObserver()
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: 10 * time.Millisecond, CreateWait: time.Second})
	s.SetObserver(obs)

	if _, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	added, _, _, bytes, _ := obs.snapshot()
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if bytes <= 0 {
		t.Fatalf("bytes = %d, want positive", bytes)
	}

	// Force lazy eviction by reading after the TTL elapses.
	time.Sleep(20 * time.Millisecond)
	s.Get("k")

	added, removed, ttl, bytes, _ := obs.snapshot()
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if ttl != 1 {
		t.Fatalf("ttl = %d, want 1", ttl)
	}
	if bytes != 0 {
		t.Fatalf("bytes = %d, want 0 after eviction", bytes)
	}
	_ = added
}

// TestObserverRecordsCapacityFailure verifies the observer is notified when the
// store refuses a record because capacity is exhausted.
func TestObserverRecordsCapacityFailure(t *testing.T) {
	obs := newRecordingObserver()
	s := NewMemory(Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	s.SetObserver(obs)

	if _, created, err := s.Create(context.Background(), "k1", build(rec("a", "m"))); err != nil || !created {
		t.Fatalf("first Create = created %v, err %v", created, err)
	}
	if _, _, err := s.Create(context.Background(), "k2", build(rec("b", "n"))); err == nil {
		t.Fatal("second Create should fail at capacity")
	}
	_, _, _, _, failures := obs.snapshot()
	if failures[StoreFailCapacity] != 1 {
		t.Fatalf("capacity failures = %d, want 1", failures[StoreFailCapacity])
	}
}