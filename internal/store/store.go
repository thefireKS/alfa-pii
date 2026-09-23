// Package store keeps correspondences between originals and masks. The
// in-memory implementation enforces entry, per-record and total byte limits,
// a time-to-live, and atomic creation of a single key. Concurrent creators
// for the same key are coordinated through a per-key reservation so that only
// one record is ever published and every caller observes the same result.
package store

import (
	"container/heap"
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

// Failure reasons reported to the observer.
const (
	StoreFailCapacity = "capacity"
	StoreFailBusy     = "busy"
)

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

// Replacement maps a marker to the byte range [Start, End) of the original
// value it stands for. The range points into the record's Original text, so the
// entity value is not duplicated in the table: the record holds the full
// original once and the table only the markers and offsets. This keeps the
// per-record size bounded for texts with many entities.
type Replacement struct {
	Marker string
	Start  int
	End    int
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

// expiryEntry is one element of the min-heap that orders records by their
// expiration time. The heap lets the store find expired records without walking
// the whole map on every access: the top of the heap is the next record to
// expire, so eviction is bounded by the number of actually expired records.
type expiryEntry struct {
	expiresAt time.Time
	key       string
}

// expiryHeap is a min-heap ordered by expiresAt.
type expiryHeap []expiryEntry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(expiryEntry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// Observer receives store lifecycle events for observability. It is optional;
// a nil observer disables reporting. Implementations must be safe for
// concurrent use because events are reported from multiple goroutines. The
// interface is declared here because the store is the caller of the observer.
type Observer interface {
	// RecordAdded is called when a new correspondence is published.
	RecordAdded()
	// RecordRemoved is called when a correspondence is evicted.
	RecordRemoved()
	// BytesDelta adjusts the accounted store bytes by delta.
	BytesDelta(delta int64)
	// TTLExpired is called when a correspondence is evicted because its TTL
	// elapsed.
	TTLExpired()
	// Failure is called when the store refuses an operation for the given
	// reason (capacity or busy).
	Failure(reason string)
}

// Memory is a concurrency-safe in-memory Store.
type Memory struct {
	mu       sync.Mutex
	entries  map[string]Record
	bytes    int64
	limits   Limits
	now      func() time.Time
	inflight map[string]*inflight
	expiries expiryHeap
	obs      Observer

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
		expiries: make(expiryHeap, 0),
	}
}

// SetObserver attaches an optional observer for lifecycle events. It must be
// called before the store is used concurrently.
func (s *Memory) SetObserver(o Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = o
}

// Get implements Store. It checks the TTL of only the requested key, so the
// cost of a read does not grow with the number of stored records. An expired
// record for the requested key is removed eagerly (O(1)); other expired records
// are reclaimed by the background cleanup or by a capacity drain.
func (s *Memory) Get(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.entries[key]
	if !ok {
		return Record{}, false
	}
	if s.isExpired(rec) {
		size := recordSize(key, rec)
		delete(s.entries, key)
		s.bytes -= size
		s.reportRemoved(size)
		s.reportTTLExpired()
		return Record{}, false
	}
	return rec, true
}

// isExpired reports whether rec has outlived the store TTL. It must be called
// with the mutex held.
func (s *Memory) isExpired(rec Record) bool {
	return s.limits.TTL > 0 && rec.CreatedAt.Before(s.now().Add(-s.limits.TTL))
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
		if rec, ok := s.entries[key]; ok {
			if s.isExpired(rec) {
				// The record outlived its TTL: treat the key as absent and
				// remove it so a new correspondence can be created. The heap
				// entry for the old record becomes stale and is skipped on the
				// next drain.
				size := recordSize(key, rec)
				delete(s.entries, key)
				s.bytes -= size
				s.reportRemoved(size)
				s.reportTTLExpired()
			} else {
				s.mu.Unlock()
				return rec, false, nil
			}
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

		// Reject before running build when the store is already at capacity, so
		// an expensive recognition is not paid for a request that cannot be
		// stored. Expired records are drained first so a full store that only
		// holds expired correspondences can accept new ones. The per-record
		// size is still checked after build.
		s.mu.Lock()
		s.drainExpired()
		if len(s.entries) >= s.limits.MaxEntries || s.bytes >= s.limits.MaxBytes {
			s.mu.Unlock()
			s.finishInflight(key, inf, Record{}, ErrCapacity)
			s.reportFailure(StoreFailCapacity)
			return Record{}, false, ErrCapacity
		}
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
		s.drainExpired()
		rec.CreatedAt = s.now()
		size := recordSize(key, rec)
		if size > s.limits.MaxRecordBytes || s.bytes+size > s.limits.MaxBytes || len(s.entries) >= s.limits.MaxEntries {
			s.mu.Unlock()
			s.finishInflight(key, inf, Record{}, ErrCapacity)
			s.reportFailure(StoreFailCapacity)
			return Record{}, false, ErrCapacity
		}
		s.entries[key] = rec
		s.bytes += size
		if s.limits.TTL > 0 {
			heap.Push(&s.expiries, expiryEntry{expiresAt: rec.CreatedAt.Add(s.limits.TTL), key: key})
		}
		s.mu.Unlock()
		s.finishInflight(key, inf, rec, nil)
		s.reportAdded(size)
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
		s.reportFailure(StoreFailBusy)
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

// cleanupLoop periodically evicts expired records until Stop is called. The
// eviction is bounded by the number of actually expired records because it
// drains the expiry heap instead of walking the whole map.
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
			s.drainExpired()
			s.mu.Unlock()
		}
	}
}

// drainExpired removes records whose TTL has elapsed, in expiry order. It pops
// the expiry heap while its top is expired and deletes the matching live
// record. Heap entries whose record was already removed or re-created are
// skipped, so the drain is bounded by the number of expired records and never
// walks the whole map. It must be called with the mutex held.
func (s *Memory) drainExpired() {
	if s.limits.TTL <= 0 {
		return
	}
	now := s.now()
	cutoff := now.Add(-s.limits.TTL)
	for s.expiries.Len() > 0 {
		top := s.expiries[0]
		if !top.expiresAt.Before(now) {
			break
		}
		heap.Pop(&s.expiries)
		rec, ok := s.entries[top.key]
		if !ok {
			continue
		}
		// A stale heap entry for a re-created key points at a record that is
		// still live; leave it for its own (later) expiry entry.
		if !rec.CreatedAt.Before(cutoff) {
			continue
		}
		size := recordSize(top.key, rec)
		delete(s.entries, top.key)
		s.bytes -= size
		s.reportRemoved(size)
		s.reportTTLExpired()
	}
}

// reportAdded notifies the observer that a record was published. It must be
// called without the mutex held.
func (s *Memory) reportAdded(size int64) {
	if s.obs == nil {
		return
	}
	s.obs.RecordAdded()
	s.obs.BytesDelta(size)
}

// reportRemoved notifies the observer that a record was evicted. It must be
// called without the mutex held.
func (s *Memory) reportRemoved(size int64) {
	if s.obs == nil {
		return
	}
	s.obs.RecordRemoved()
	s.obs.BytesDelta(-size)
}

// reportTTLExpired notifies the observer of a TTL eviction. It must be called
// without the mutex held.
func (s *Memory) reportTTLExpired() {
	if s.obs == nil {
		return
	}
	s.obs.TTLExpired()
}

// reportFailure notifies the observer of a storage failure. It must be called
// without the mutex held.
func (s *Memory) reportFailure(reason string) {
	if s.obs == nil {
		return
	}
	s.obs.Failure(reason)
}

// recordSize estimates the bytes a record occupies in memory, including the
// held key, the original text, the mask, the replacement table and fixed
// overhead. The key is counted because a payload_id can be large enough to
// dominate the request body and is stored as the map key. Each table entry
// counts only its marker and fixed overhead: the original value is a range into
// the record's Original text and is not duplicated.
func recordSize(key string, rec Record) int64 {
	size := int64(recordOverhead + len(key) + len(rec.Original) + len(rec.Masked))
	for _, r := range rec.Table {
		size += int64(replacementOverhead + len(r.Marker))
	}
	return size
}
