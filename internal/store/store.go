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

// Failure reasons reported to the observer. The set is fixed and bounded so
// metrics labels stay limited. Capacity refusals are broken down by the limit
// that was hit so diagnostics can tell an entry limit from a byte budget or a
// single-record limit apart.
const (
	StoreFailCapacity    = "capacity"     // generic capacity refusal
	StoreFailBusy        = "busy"         // wait for another creator timed out
	StoreFailEntries     = "entries"      // entry limit reached
	StoreFailBytes       = "bytes"        // total byte budget reached
	StoreFailRecordBytes = "record_bytes" // single record exceeds its limit
)

// Phases of a stored correspondence. A record is created in the pending phase
// (awaiting its first restore) and moves to the replay phase after the first
// successful restore, where it is kept for the replay window so a lost response
// can be repeated. The phase is a fixed label used for observability; it is
// never derived from user data.
const (
	PhasePending = "pending"
	PhaseReplay  = "replay"
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
	// FirstRestoreAt is the wall-clock time of the first successful restore.
	// A zero value means the record is still in the pending phase and expires
	// after the store TTL. Once set, the record is in the replay phase and
	// expires after FirstRestoreAt + ReplayTTL.
	FirstRestoreAt time.Time
	// Version is a monotonically increasing identifier assigned at publish
	// time. It lets a caller tie a restore transition to the exact record it
	// read: a late request holding an old version cannot complete a newer
	// record that was re-created for the same key after expiry.
	Version uint64
	// size is the estimated memory footprint of the record, computed once at
	// publish time and reused at eviction so the replacement table is not
	// re-walked under the store mutex. It is unexported because it is an
	// internal accounting detail, not part of the stored correspondence.
	size int64
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
	//
	// minSize is the justified lower bound of the record's memory footprint
	// known before recognition (the held key and the original text). The store
	// reserves minSize plus the inflight overhead against the same MaxBytes and
	// MaxEntries budget as stored records before running build, so concurrent
	// creators for different keys cannot collectively exceed the budget and a
	// request that cannot possibly fit is rejected before the expensive
	// recognition runs. The real size is checked after build; if it does not
	// fit, the reservation is released and ErrCapacity is returned.
	Create(ctx context.Context, key string, minSize int64, build func(context.Context) (Record, error)) (Record, bool, error)
	// MarkRestored transitions the record for key from the pending phase to the
	// replay phase, extending its lifetime to FirstRestoreAt + ReplayTTL. It
	// returns true only if the transition happened: the record exists, its
	// version matches version (so a late request cannot complete a newer record
	// re-created for the same key), and it was not already in the replay phase.
	// A repeat of the original or mask within the replay window does not extend
	// the deadline, so MarkRestored is a no-op once the record is in replay.
	MarkRestored(key string, version uint64) bool
}

// Limits bound the in-memory store.
type Limits struct {
	// MaxEntries caps the number of stored correspondences.
	MaxEntries int
	// MaxBytes caps the total bytes held by all correspondences.
	MaxBytes int64
	// MaxRecordBytes caps the estimated bytes of a single correspondence.
	MaxRecordBytes int64
	// TTL is how long a correspondence is kept after creation while it awaits
	// its first restore (the pending phase).
	TTL time.Duration
	// ReplayTTL is how long a correspondence is kept after its first successful
	// restore (the replay phase), so a lost response can be repeated. A repeat
	// within the window does not extend the deadline.
	ReplayTTL time.Duration
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

// inflightOverhead approximates the memory held by one active reservation: the
// inflight struct, its done channel and the key string in the inflight map. It
// is reserved alongside the record's lower bound so concurrent creators for
// different keys cannot collectively exceed the byte budget with their
// coordination state.
const inflightOverhead = 64

// MinRecordSize returns the justified lower bound of a record's memory
// footprint known before recognition: the fixed overhead, the held key and the
// original text. The masked text and replacement table are not known yet, so
// they are not counted; the real size is checked after build. The caller
// supplies the original length because the store does not see the payload
// before build runs.
func MinRecordSize(key string, originalLen int) int64 {
	return int64(recordOverhead + len(key) + originalLen)
}

// inflight is a per-key reservation. The winning creator publishes its result
// through done; waiters block on done and read rec/err.
type inflight struct {
	done chan struct{}
	rec  Record
	err  error
}

// expiryNode is one element of the expiry queue.
type expiryNode struct {
	expiresAt time.Time
	key       string
}

// expiryQueue is a min-heap of expiry nodes ordered by expiresAt, with an index
// map so a node can be removed in O(log n) when its record is deleted or
// re-created. Each live record has exactly one node in the queue: removing a
// record removes its node, so the queue never accumulates stale key references
// and its memory is bounded by the number of live records. Pop zeroes the
// removed backing-array slot so a long key is not retained after eviction.
type expiryQueue struct {
	nodes []expiryNode
	index map[string]int
}

func newExpiryQueue() *expiryQueue {
	return &expiryQueue{index: make(map[string]int)}
}

func (q *expiryQueue) Len() int { return len(q.nodes) }
func (q *expiryQueue) Less(i, j int) bool {
	return q.nodes[i].expiresAt.Before(q.nodes[j].expiresAt)
}
func (q *expiryQueue) Swap(i, j int) {
	q.nodes[i], q.nodes[j] = q.nodes[j], q.nodes[i]
	q.index[q.nodes[i].key] = i
	q.index[q.nodes[j].key] = j
}
func (q *expiryQueue) Push(x any) {
	n := x.(expiryNode)
	q.index[n.key] = len(q.nodes)
	q.nodes = append(q.nodes, n)
}
func (q *expiryQueue) Pop() any {
	old := q.nodes
	n := len(old)
	item := old[n-1]
	old[n-1] = expiryNode{} // release the key reference from the backing array
	q.nodes = old[:n-1]
	delete(q.index, item.key)
	return item
}

// remove deletes the node for key from the queue, if present. It is used when a
// record is evicted or re-created so the queue holds exactly one node per live
// record.
func (q *expiryQueue) remove(key string) {
	i, ok := q.index[key]
	if !ok {
		return
	}
	heap.Remove(q, i)
}

// Observer receives store lifecycle events for observability. It is optional;
// a nil observer disables reporting. Implementations must be safe for
// concurrent use because events are reported from multiple goroutines. The
// interface is declared here because the store is the caller of the observer.
// Each event carries the record phase (pending or replay) so metrics can be
// broken down by phase; the area (process or managed) is fixed per store and is
// supplied by the observer implementation, not by the store.
type Observer interface {
	// RecordAdded is called when a new correspondence is published.
	RecordAdded(phase string)
	// RecordRemoved is called when a correspondence is evicted.
	RecordRemoved(phase string)
	// BytesDelta adjusts the accounted store bytes for the phase by delta.
	BytesDelta(phase string, delta int64)
	// TTLExpired is called when a correspondence is evicted because its TTL
	// elapsed.
	TTLExpired(phase string)
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
	expiries *expiryQueue
	obs      Observer
	nextVer  uint64

	// reservedBytes and reservedEntries account for capacity held by active
	// creators that have not yet published. They share the same MaxBytes and
	// MaxEntries budget as stored records, so concurrent creators for different
	// keys cannot collectively exceed the budget. A reservation is released
	// exactly once on every outcome: publish, build error, cancellation or a
	// post-build capacity refusal.
	reservedBytes   int64
	reservedEntries int

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
		expiries: newExpiryQueue(),
	}
}

// SetObserver attaches an optional observer for lifecycle events. It must be
// called before the store is used concurrently.
func (s *Memory) SetObserver(o Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = o
}

// SetClock replaces the store's time source. It is a testing seam for
// deterministic TTL and replay-window tests; production code never calls it.
// It must be called before the store is used concurrently.
func (s *Memory) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Get implements Store. It checks the TTL of only the requested key, so the
// cost of a read does not grow with the number of stored records. An expired
// record for the requested key is removed eagerly (O(log n)); other expired
// records are reclaimed by the background cleanup or by a capacity drain.
func (s *Memory) Get(key string) (Record, bool) {
	s.mu.Lock()
	rec, ok := s.entries[key]
	if !ok {
		s.mu.Unlock()
		return Record{}, false
	}
	if s.isExpired(rec) {
		ev := s.evictLocked(key, rec)
		s.mu.Unlock()
		s.flushEvents(ev)
		return Record{}, false
	}
	s.mu.Unlock()
	return rec, true
}

// isExpired reports whether rec has outlived its current phase deadline. A
// record in the pending phase expires after the store TTL; a record in the
// replay phase expires after FirstRestoreAt + ReplayTTL. A non-positive TTL
// disables expiry entirely, matching the store's "no TTL" configuration. It
// must be called with the mutex held.
func (s *Memory) isExpired(rec Record) bool {
	if s.limits.TTL <= 0 {
		return false
	}
	return !s.recordExpiry(rec).After(s.now())
}

// recordExpiry returns the wall-clock deadline at which rec expires, based on
// its phase. It must be called with the mutex held.
func (s *Memory) recordExpiry(rec Record) time.Time {
	if !rec.FirstRestoreAt.IsZero() {
		return rec.FirstRestoreAt.Add(s.limits.ReplayTTL)
	}
	return rec.CreatedAt.Add(s.limits.TTL)
}

// Create implements Store. It is atomic for a single key: concurrent calls for
// the same key yield the same winning record. The global mutex is not held
// while build runs, so different keys are processed independently and a large
// recognition does not block unrelated keys.
//
// Before build runs, the store reserves minSize plus the inflight overhead
// against the shared MaxBytes and MaxEntries budget. This rejects a request
// that cannot possibly fit before the expensive recognition is paid, and it
// prevents concurrent creators for different keys from collectively exceeding
// the budget. After build the real size is checked; a refusal, a build error or
// a cancellation releases the reservation exactly once.
func (s *Memory) Create(ctx context.Context, key string, minSize int64, build func(context.Context) (Record, error)) (Record, bool, error) {
	reserve := minSize + inflightOverhead
	for {
		if err := ctx.Err(); err != nil {
			return Record{}, false, err
		}
		s.mu.Lock()
		if rec, ok := s.entries[key]; ok {
			if s.isExpired(rec) {
				// The record outlived its TTL: treat the key as absent and
				// remove it so a new correspondence can be created. Its expiry
				// node is removed too, so the queue keeps one node per live
				// record. Restart the loop to re-acquire the lock and become
				// the creator.
				ev := s.evictLocked(key, rec)
				s.mu.Unlock()
				s.flushEvents(ev)
				continue
			}
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
				// The previous creator was cancelled by its own context without
				// publishing; retry and become the creator ourselves. A
				// persistent error is returned to the waiter instead, so the
				// expensive recognition is not repeated for a failure that will
				// not change.
				continue
			}
			return rec, false, nil
		}
		// Become the creator for this key. The reservation is taken under the
		// same lock that publishes the inflight entry, so no other goroutine
		// can observe the reservation before it is fully accounted.
		inf := &inflight{done: make(chan struct{})}
		s.inflight[key] = inf

		// Reject before running build when the store cannot hold even the
		// lower bound of the record, so an expensive recognition is not paid
		// for a request that cannot be stored. A bounded batch of expired
		// records is drained first so a full store that only holds expired
		// correspondences can accept new ones; the drain never walks the whole
		// backlog under the mutex. The per-record size is still checked after
		// build.
		ev := s.drainExpired()
		if reason := s.reserveCapacityLocked(reserve); reason != "" {
			// No waiter can have observed the inflight entry yet because the
			// lock was held throughout, so the entry is removed without waking
			// anyone.
			delete(s.inflight, key)
			s.mu.Unlock()
			s.flushEvents(ev)
			s.reportFailure(reason)
			return Record{}, false, ErrCapacity
		}
		s.mu.Unlock()
		s.flushEvents(ev)

		rec, err := build(ctx)
		if err != nil {
			s.failInflight(key, inf, reserve, err)
			return Record{}, false, err
		}
		if cerr := ctx.Err(); cerr != nil {
			s.failInflight(key, inf, reserve, cerr)
			return Record{}, false, cerr
		}

		s.mu.Lock()
		ev = s.drainExpired()
		rec.CreatedAt = s.now()
		rec.size = recordSize(key, rec)
		size := rec.size
		if reason := s.publishCapacityLocked(size, reserve); reason != "" {
			s.releaseReservationLocked(key, inf, reserve)
			s.mu.Unlock()
			s.flushEvents(ev)
			s.finishInflight(key, inf, Record{}, ErrCapacity)
			s.reportFailure(reason)
			return Record{}, false, ErrCapacity
		}
		// Publish: atomically replace the reservation with the stored record.
		// The entry slot and the reserved bytes are converted into the record,
		// so the shared budget is unchanged by the transition.
		s.reservedBytes -= reserve
		s.reservedEntries--
		s.nextVer++
		rec.Version = s.nextVer
		s.entries[key] = rec
		s.bytes += size
		if s.limits.TTL > 0 {
			heap.Push(s.expiries, expiryNode{expiresAt: rec.CreatedAt.Add(s.limits.TTL), key: key})
		}
		s.mu.Unlock()
		s.flushEvents(ev)
		s.finishInflight(key, inf, rec, nil)
		s.reportAdded(size)
		return rec, true, nil
	}
}

// reserveCapacityLocked attempts to reserve reserve bytes and one entry slot
// against the shared budget. It returns the failure reason on refusal, or ""
// on success. It must be called with the mutex held.
func (s *Memory) reserveCapacityLocked(reserve int64) string {
	if s.limits.MaxEntries > 0 && len(s.entries)+s.reservedEntries >= s.limits.MaxEntries {
		return StoreFailEntries
	}
	if s.limits.MaxBytes > 0 && s.bytes+s.reservedBytes+reserve > s.limits.MaxBytes {
		return StoreFailBytes
	}
	s.reservedBytes += reserve
	s.reservedEntries++
	return ""
}

// publishCapacityLocked checks whether the real record size fits after the
// reservation is replaced by the stored record. It returns the failure reason
// on refusal, or "" on success. The entry slot is already held by the
// reservation, so only the per-record and byte limits are re-checked. It must
// be called with the mutex held.
func (s *Memory) publishCapacityLocked(size, reserve int64) string {
	if s.limits.MaxRecordBytes > 0 && size > s.limits.MaxRecordBytes {
		return StoreFailRecordBytes
	}
	if s.limits.MaxBytes > 0 && s.bytes+s.reservedBytes-reserve+size > s.limits.MaxBytes {
		return StoreFailBytes
	}
	return ""
}

// releaseReservationLocked returns the reservation to the shared budget. It
// must be called with the mutex held and only for a reservation that was
// actually taken.
func (s *Memory) releaseReservationLocked(key string, inf *inflight, reserve int64) {
	if s.inflight[key] == inf {
		delete(s.inflight, key)
	}
	s.reservedBytes -= reserve
	s.reservedEntries--
}

// waitInflight blocks until the creator publishes or fails, bounded by ctx and
// the store's CreateWait. It returns created=true when the creator was cancelled
// by its own context without publishing, so a waiter with a still-live context
// should retry and become the creator itself. A persistent error (a build
// failure or a capacity refusal) is returned to every waiter unchanged, so the
// expensive recognition is not repeated for a failure that will not change.
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
			if isContextError(inf.err) {
				// The creator was cancelled by its own context; a waiter with a
				// live context may retry creation. A waiter whose own context is
				// cancelled returns its own context error at the top of the
				// Create loop.
				return Record{}, true, nil
			}
			return Record{}, false, inf.err
		}
		return inf.rec, false, nil
	case <-ctx.Done():
		return Record{}, false, ctx.Err()
	case <-timeout:
		s.reportFailure(StoreFailBusy)
		return Record{}, false, ErrBusy
	}
}

// isContextError reports whether err is a context cancellation or deadline
// exceeded. It is used to tell a creator cancelled by its own context apart
// from a persistent build or capacity error, so waiters only retry creation
// when the failure is transient.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// failInflight releases the reservation and wakes all waiters with the error.
// It is used for a build error, a cancellation after build, or a post-build
// capacity refusal. The reservation is released exactly once.
func (s *Memory) failInflight(key string, inf *inflight, reserve int64, err error) {
	s.mu.Lock()
	s.releaseReservationLocked(key, inf, reserve)
	inf.rec = Record{}
	inf.err = err
	close(inf.done)
	s.mu.Unlock()
}

// finishInflight removes the reservation and wakes all waiters with the result.
// It is used on the publish path, where the reservation was already replaced by
// the stored record in the publish critical section, so no reservation is
// released here.
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

// MarkRestored implements Store. It atomically moves the record for key from
// the pending phase to the replay phase, extending its lifetime to
// FirstRestoreAt + ReplayTTL. The transition is tied to the record version the
// caller read: if the record was re-created for the same key after expiry, the
// caller's version no longer matches and the newer record is left untouched. A
// record already in the replay phase is not extended again, so repeated
// restores within the window do not push the deadline. The expiry queue keeps
// exactly one node per live record: the pending node is removed and the replay
// node is pushed, so no stale nodes accumulate on repeats.
func (s *Memory) MarkRestored(key string, version uint64) bool {
	s.mu.Lock()
	rec, ok := s.entries[key]
	if !ok || rec.Version != version || !rec.FirstRestoreAt.IsZero() || s.limits.ReplayTTL <= 0 {
		s.mu.Unlock()
		return false
	}
	rec.FirstRestoreAt = s.now()
	s.entries[key] = rec
	s.expiries.remove(key)
	// ReplayTTL is positive here (the guard above rejects ReplayTTL <= 0), so
	// the replay expiry node is always pushed.
	heap.Push(s.expiries, expiryNode{expiresAt: rec.FirstRestoreAt.Add(s.limits.ReplayTTL), key: key})
	ev := pendingEvents{
		removedPending: 1,
		bytesPending:   -rec.size,
	}
	s.mu.Unlock()
	s.flushEvents(ev)
	if s.obs != nil {
		s.obs.RecordAdded(PhaseReplay)
		s.obs.BytesDelta(PhaseReplay, rec.size)
	}
	return true
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
// safe to call when cleanup was never started and safe to call repeatedly: only
// the first call stops the goroutine, and every caller that actually stops it
// waits for completion. The done channel is captured under the lock so a
// concurrent StartCleanup cannot make Stop wait on a newer goroutine.
func (s *Memory) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	close(s.stopCh)
	done := s.doneCh
	s.mu.Unlock()
	<-done
}

// cleanupLoop periodically evicts expired records until Stop is called. Each
// tick drains the expiry queue in bounded batches, releasing the mutex between
// batches so concurrent operations are not starved by a large backlog.
func (s *Memory) cleanupLoop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.limits.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.drainAll()
		}
	}
}

// drainAll repeatedly drains bounded batches of expired records until none
// remain, releasing the mutex and flushing observer events between batches. It
// is used by the background cleanup so a large backlog is reclaimed without
// holding the store mutex for the whole pass.
func (s *Memory) drainAll() {
	for {
		s.mu.Lock()
		ev := s.drainExpired()
		s.mu.Unlock()
		s.flushEvents(ev)
		if ev.removedPending+ev.removedReplay == 0 {
			return
		}
	}
}

// drainBatchSize bounds the number of expired records removed in one pass under
// the store mutex. A single Create or cleanup tick reclaims at most this many
// records, so a large accumulated backlog is processed in batches and the lock
// is released between them.
const drainBatchSize = 1000

// drainExpired removes up to drainBatchSize expired records in expiry order and
// returns the observer events to flush. It must be called with the mutex held.
// The batch bound keeps a single pass under the mutex short so concurrent
// operations are not starved; the background cleanup repeats the drain until no
// expired records remain.
func (s *Memory) drainExpired() pendingEvents {
	var ev pendingEvents
	if s.limits.TTL <= 0 {
		return ev
	}
	now := s.now()
	for i := 0; i < drainBatchSize && s.expiries.Len() > 0; i++ {
		top := s.expiries.nodes[0]
		if top.expiresAt.After(now) {
			break
		}
		heap.Pop(s.expiries)
		rec, ok := s.entries[top.key]
		if !ok {
			continue
		}
		// A stale node for a re-created key points at a record that is still
		// live; leave it for its own (later) expiry node. With the indexed
		// queue this should not occur, but the check keeps eviction safe. The
		// record's own phase deadline is authoritative, so a replay-phase
		// record is not skipped just because its CreatedAt is recent.
		if s.recordExpiry(rec).After(now) {
			continue
		}
		ev.remove(rec)
		delete(s.entries, top.key)
		s.bytes -= rec.size
	}
	return ev
}

// evictLocked removes a record and its expiry node from the store and returns
// the observer events to flush. It must be called with the mutex held.
func (s *Memory) evictLocked(key string, rec Record) pendingEvents {
	delete(s.entries, key)
	s.bytes -= rec.size
	s.expiries.remove(key)
	ev := pendingEvents{}
	ev.remove(rec)
	return ev
}

// pendingEvents accumulates observer notifications to be flushed after the
// store mutex is released, so the observer is never called under the lock. The
// counts and byte deltas are bounded (no per-event queue), so concurrent adds
// and removals keep the final counters accurate without an unbounded event
// buffer. Events are tracked per phase so metrics can be broken down by
// pending and replay.
type pendingEvents struct {
	removedPending int
	removedReplay  int
	ttlPending     int
	ttlReplay      int
	bytesPending   int64
	bytesReplay    int64
}

// remove records the eviction of a record in the given phase, including its
// byte release and, for a TTL eviction, the TTL counter. It is used by both
// lazy eviction and the expiry drain.
func (ev *pendingEvents) remove(rec Record) {
	if !rec.FirstRestoreAt.IsZero() {
		ev.removedReplay++
		ev.ttlReplay++
		ev.bytesReplay -= rec.size
		return
	}
	ev.removedPending++
	ev.ttlPending++
	ev.bytesPending -= rec.size
}

// flushEvents delivers accumulated observer notifications. It must be called
// without the mutex held.
func (s *Memory) flushEvents(ev pendingEvents) {
	if s.obs == nil {
		return
	}
	for i := 0; i < ev.removedPending; i++ {
		s.obs.RecordRemoved(PhasePending)
	}
	for i := 0; i < ev.removedReplay; i++ {
		s.obs.RecordRemoved(PhaseReplay)
	}
	for i := 0; i < ev.ttlPending; i++ {
		s.obs.TTLExpired(PhasePending)
	}
	for i := 0; i < ev.ttlReplay; i++ {
		s.obs.TTLExpired(PhaseReplay)
	}
	if ev.bytesPending != 0 {
		s.obs.BytesDelta(PhasePending, ev.bytesPending)
	}
	if ev.bytesReplay != 0 {
		s.obs.BytesDelta(PhaseReplay, ev.bytesReplay)
	}
}

// reportAdded notifies the observer that a record was published in the pending
// phase. It must be called without the mutex held.
func (s *Memory) reportAdded(size int64) {
	if s.obs == nil {
		return
	}
	s.obs.RecordAdded(PhasePending)
	s.obs.BytesDelta(PhasePending, size)
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
