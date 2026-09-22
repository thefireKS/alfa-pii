// Package store keeps correspondences between originals and masks. The
// in-memory implementation enforces entry, per-record and total byte limits,
// a time-to-live, and atomic creation of a single key. Concurrent creators
// for the same key are coordinated through a per-key reservation so that only
// one record is ever published and every caller observes the same result.
package store

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCapacity is returned when the store cannot accept a new correspondence
// because an entry, per-record or total byte limit would be exceeded.
var ErrCapacity = errors.New("store capacity exceeded")

// ErrBusy is returned when a caller waits too long for another creator of the
// same key to publish its result.
var ErrBusy = errors.New("store busy creating key")

// Record is a stored correspondence for one key.
type Record struct {
	// Original is the unmasked text.
	Original string
	// Masked is the masked text.
	Masked string
	// Table maps markers to original values for restoration.
	Table []Replacement
	// Format is the mask format used to produce Masked. It is bound to the
	// record so restoration uses the same format that created the mask.
	Format string
	// CreatedAt is the wall-clock time the record was created.
	CreatedAt time.Time
}

// Replacement maps a marker to the original value it stands for.
type Replacement struct {
	Marker   string
	Original string
}

// Store is the storage contract used by the application layer.
type Store interface {
	// Get returns the record for key, if present and not expired.
	Get(key string) (Record, bool)
	// Create atomically inserts the record produced by build for key. If key
	// already holds a live record, the existing record is returned with
	// created=false. If another goroutine is creating the same key, the caller
	// waits for it (bounded by ctx and the store's CreateWait) and returns the
	// published record. build is invoked only by the winning creator, so a
	// waiter never repeats the expensive recognition work. If the store is at
	// capacity, ErrCapacity is returned; if the wait times out, ErrBusy is
	// returned.
	Create(ctx context.Context, key string, build func(context.Context) (Record, error)) (Record, bool, error)
}

// Limits bound the in-memory store.
type Limits struct {
	// MaxEntries caps the number of stored correspondences.
	MaxEntries int
	// MaxBytes caps the total bytes held by all correspondences.
	MaxBytes int64
	// MaxRecordBytes caps the estimated bytes of a single correspondence.
	MaxRecordBytes int64
	// TTL is how long a correspondence is kept after creation.
	TTL time.Duration
	// CreateWait is the maximum time a caller waits for another creator of the
	// same key before returning ErrBusy.
	CreateWait time.Duration
	// CleanupInterval is how often the background cleanup goroutine evicts
	// expired records. A non-positive value disables the background goroutine;
	// expired records are then evicted lazily on access.
	CleanupInterval time.Duration
}

// recordOverhead approximates the fixed per-record memory cost: the map entry,
// the Record struct and the slice header of the replacement table. The byte
// accounting is intentionally approximate and is validated against RSS.
const recordOverhead = 128

// replacementOverhead approximates the per-replacement slice element cost.
const replacementOverhead = 32

// inflight is a per-key reservation. The winning creator publishes its result
// through done; waiters block on done and read rec/err.
type inflight struct {
	done chan struct{}
	rec  Record
	err  error
}

// Memory is a concurrency-safe in-memory Store.
type Memory struct {
	mu       sync.Mutex
	entries  map[string]Record
	bytes    int64
	limits   Limits
	now      func() time.Time
	inflight map[string]*inflight

	stopCh  chan struct{}
	doneCh  chan struct{}
	started bool
}

// NewMemory returns an empty in-memory store with the given limits.
func NewMemory(limits Limits) *Memory {
	return &Memory{
		entries:  make(map[string]Record),
		limits:   limits,
		now:      time.Now,
		inflight: make(map[string]*inflight),
	}
}

// Get implements Store.
func (s *Memory) Get(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired()
	rec, ok := s.entries[key]
	return rec, ok
}

// Create implements Store. It is atomic for a single key: concurrent calls for
// the same key yield the same winning record. The global mutex is not held
// while build runs, so different keys are processed independently and a large
// recognition does not block unrelated keys.
func (s *Memory) Create(ctx context.Context, key string, build func(context.Context) (Record, error)) (Record, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Record{}, false, err
		}
		s.mu.Lock()
		s.evictExpired()
		if rec, ok := s.entries[key]; ok {
			s.mu.Unlock()
			return rec, false, nil
		}
		if inf, ok := s.inflight[key]; ok {
			s.mu.Unlock()
			rec, created, err := s.waitInflight(ctx, inf)
			if err != nil {
				return Record{}, false, err
			}
			if created {
				// The previous creator failed or was cancelled without
				// publishing; retry and become the creator ourselves.
				continue
			}
			return rec, false, nil
		}
		// Become the creator for this key.
		inf := &inflight{done: make(chan struct{})}
		s.inflight[key] = inf
		s.mu.Unlock()

		rec, err := build(ctx)
		if err != nil {
			s.finishInflight(key, inf, Record{}, err)
			return Record{}, false, err
		}
		if cerr := ctx.Err(); cerr != nil {
			s.finishInflight(key, inf, Record{}, cerr)
			return Record{}, false, cerr
		}

		s.mu.Lock()
		s.evictExpired()
		rec.CreatedAt = s.now()
		size := recordSize(rec)
		if size > s.limits.MaxRecordBytes || s.bytes+size > s.limits.MaxBytes || len(s.entries) >= s.limits.MaxEntries {
			s.mu.Unlock()
			s.finishInflight(key, inf, Record{}, ErrCapacity)
			return Record{}, false, ErrCapacity
		}
		s.entries[key] = rec
		s.bytes += size
		s.mu.Unlock()
		s.finishInflight(key, inf, rec, nil)
		return rec, true, nil
	}
}

// waitInflight blocks until the creator publishes or fails, bounded by ctx and
// the store's CreateWait. It returns created=true when the creator released the
// reservation without publishing, so the caller should retry.
func (s *Memory) waitInflight(ctx context.Context, inf *inflight) (Record, bool, error) {
	var timer *time.Timer
	var timeout <-chan time.Time
	if s.limits.CreateWait > 0 {
		timer = time.NewTimer(s.limits.CreateWait)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-inf.done:
		if inf.err != nil {
			return Record{}, true, nil
		}
		return inf.rec, false, nil
	case <-ctx.Done():
		return Record{}, false, ctx.Err()
	case <-timeout:
		return Record{}, false, ErrBusy
	}
}

// finishInflight removes the reservation and wakes all waiters with the result.
func (s *Memory) finishInflight(key string, inf *inflight, rec Record, err error) {
	s.mu.Lock()
	if s.inflight[key] == inf {
		delete(s.inflight, key)
	}
	inf.rec = rec
	inf.err = err
	close(inf.done)
	s.mu.Unlock()
}

// StartCleanup launches the background eviction goroutine. It is idempotent.
// The goroutine is owned by the store and is stopped and awaited via Stop.
func (s *Memory) StartCleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.limits.CleanupInterval <= 0 {
		return
	}
	s.started = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	go s.cleanupLoop()
}

// Stop stops the background cleanup goroutine and waits for it to finish. It is
// safe to call when cleanup was never started.
func (s *Memory) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	close(s.stopCh)
	s.mu.Unlock()
	<-s.doneCh
}

// cleanupLoop periodically evicts expired records until Stop is called.
func (s *Memory) cleanupLoop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.limits.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			s.evictExpired()
			s.mu.Unlock()
		}
	}
}

// evictExpired removes records whose TTL has elapsed. It must be called with
// the mutex held.
func (s *Memory) evictExpired() {
	if s.limits.TTL <= 0 {
		return
	}
	cutoff := s.now().Add(-s.limits.TTL)
	for k, rec := range s.entries {
		if rec.CreatedAt.Before(cutoff) {
			s.bytes -= recordSize(rec)
			delete(s.entries, k)
		}
	}
}

// recordSize estimates the bytes a record occupies in memory, including the
// original text, the mask, the replacement table and fixed overhead.
func recordSize(rec Record) int64 {
	size := int64(recordOverhead + len(rec.Original) + len(rec.Masked))
	for _, r := range rec.Table {
		size += int64(replacementOverhead + len(r.Marker) + len(r.Original))
	}
	return size
}
