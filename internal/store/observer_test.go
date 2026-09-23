package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingObserver captures store lifecycle events for assertions.
type recordingObserver struct {
	mu       sync.Mutex
	added    map[string]int
	removed  map[string]int
	bytes    map[string]int64
	ttl      map[string]int
	failures map[string]int
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{
		added:    make(map[string]int),
		removed:  make(map[string]int),
		bytes:    make(map[string]int64),
		ttl:      make(map[string]int),
		failures: make(map[string]int),
	}
}

func (o *recordingObserver) RecordAdded(phase string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.added[phase]++
}
func (o *recordingObserver) RecordRemoved(phase string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removed[phase]++
}
func (o *recordingObserver) BytesDelta(phase string, d int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bytes[phase] += d
}
func (o *recordingObserver) TTLExpired(phase string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ttl[phase]++
}
func (o *recordingObserver) Failure(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures[reason]++
}

func (o *recordingObserver) snapshot() (added, removed, ttl map[string]int, bytes map[string]int64, failures map[string]int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	a := make(map[string]int, len(o.added))
	for k, v := range o.added {
		a[k] = v
	}
	r := make(map[string]int, len(o.removed))
	for k, v := range o.removed {
		r[k] = v
	}
	t := make(map[string]int, len(o.ttl))
	for k, v := range o.ttl {
		t[k] = v
	}
	b := make(map[string]int64, len(o.bytes))
	for k, v := range o.bytes {
		b[k] = v
	}
	f := make(map[string]int, len(o.failures))
	for k, v := range o.failures {
		f[k] = v
	}
	return a, r, t, b, f
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
	if added[PhasePending] != 1 {
		t.Fatalf("added pending = %d, want 1", added[PhasePending])
	}
	if bytes[PhasePending] <= 0 {
		t.Fatalf("bytes pending = %d, want positive", bytes[PhasePending])
	}

	// Force lazy eviction by reading after the TTL elapses.
	time.Sleep(20 * time.Millisecond)
	s.Get("k")

	_, removed, ttl, bytes, _ := obs.snapshot()
	if removed[PhasePending] != 1 {
		t.Fatalf("removed pending = %d, want 1", removed[PhasePending])
	}
	if ttl[PhasePending] != 1 {
		t.Fatalf("ttl pending = %d, want 1", ttl[PhasePending])
	}
	if bytes[PhasePending] != 0 {
		t.Fatalf("bytes pending = %d, want 0 after eviction", bytes[PhasePending])
	}
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
