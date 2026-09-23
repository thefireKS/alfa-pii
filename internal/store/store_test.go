package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func rec(original, masked string) Record {
	return Record{Original: original, Masked: masked}
}

func build(rec Record) func(context.Context) (Record, error) {
	return func(context.Context) (Record, error) { return rec, nil }
}

func testLimits() Limits {
	return Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second}
}

func TestCreateAndGet(t *testing.T) {
	s := NewMemory(testLimits())
	got, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask")))
	if err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	if got.Original != "orig" || got.Masked != "mask" {
		t.Fatalf("got %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}
	r, ok := s.Get("k")
	if !ok || r.Original != "orig" {
		t.Fatalf("Get = %+v, %v", r, ok)
	}
}

func TestCreateExistingReturnsExisting(t *testing.T) {
	s := NewMemory(testLimits())
	_, _, _ = s.Create(context.Background(), "k", build(rec("orig", "mask")))
	got, created, err := s.Create(context.Background(), "k", build(rec("other", "othermask")))
	if err != nil || created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	if got.Original != "orig" {
		t.Fatalf("got %+v, want existing record", got)
	}
}

// TestCreateAtCapacitySkipsBuild verifies that when the store is already at
// capacity, Create returns ErrCapacity without invoking build, so an expensive
// recognition is not paid for a request that cannot be stored.
func TestCreateAtCapacitySkipsBuild(t *testing.T) {
	limits := testLimits()
	limits.MaxEntries = 1
	s := NewMemory(limits)
	if _, _, err := s.Create(context.Background(), "k1", build(rec("orig", "mask"))); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	built := false
	_, _, err := s.Create(context.Background(), "k2", func(context.Context) (Record, error) {
		built = true
		return rec("o", "m"), nil
	})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
	if built {
		t.Fatal("build was invoked for a request rejected at capacity")
	}
}

func TestConcurrentCreateSameKey(t *testing.T) {
	s := NewMemory(testLimits())
	const n = 50
	var wg sync.WaitGroup
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, created, _ := s.Create(context.Background(), "k", build(rec("orig", "mask")))
			if created {
				results[i] = "created"
			} else {
				results[i] = "existing"
			}
		}(i)
	}
	wg.Wait()
	created := 0
	for _, r := range results {
		if r == "created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d times, want exactly 1", created)
	}
}

// TestConcurrentCreateSameKeySingleBuild verifies that only one goroutine runs
// the builder for a key, so concurrent identical requests never publish
// different masks and never repeat the expensive recognition.
func TestConcurrentCreateSameKeySingleBuild(t *testing.T) {
	s := NewMemory(testLimits())
	var mu sync.Mutex
	builds := 0
	builder := func(context.Context) (Record, error) {
		mu.Lock()
		builds++
		mu.Unlock()
		return rec("orig", "mask"), nil
	}
	const n = 50
	var wg sync.WaitGroup
	masks := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _, _ := s.Create(context.Background(), "k", builder)
			masks[i] = r.Masked
		}(i)
	}
	wg.Wait()
	mu.Lock()
	b := builds
	mu.Unlock()
	if b != 1 {
		t.Fatalf("builder ran %d times, want 1", b)
	}
	for i := 1; i < n; i++ {
		if masks[i] != masks[0] {
			t.Fatalf("mask %d = %q, want %q", i, masks[i], masks[0])
		}
	}
}

// TestCreateWaiterCancelled verifies that cancelling a waiting request does not
// disturb the creator, which still publishes its result.
func TestCreateWaiterCancelled(t *testing.T) {
	s := NewMemory(testLimits())
	release := make(chan struct{})
	creatorDone := make(chan struct{})
	go func() {
		defer close(creatorDone)
		_, _, _ = s.Create(context.Background(), "k", func(context.Context) (Record, error) {
			<-release
			return rec("orig", "mask"), nil
		})
	}()

	// Give the creator time to reserve the key.
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := s.Create(ctx, "k", build(rec("x", "y")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}

	close(release)
	<-creatorDone
	r, ok := s.Get("k")
	if !ok || r.Masked != "mask" {
		t.Fatalf("creator result lost: %+v, %v", r, ok)
	}
}

// TestCreateCreatorCancelledNoDanglingReservation verifies that cancelling the
// creator releases the reservation so a later request can create the key.
func TestCreateCreatorCancelledNoDanglingReservation(t *testing.T) {
	s := NewMemory(testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _, _ = s.Create(ctx, "k", func(c context.Context) (Record, error) {
			close(started)
			<-release
			return rec("orig", "mask"), nil
		})
	}()
	<-started
	cancel()
	close(release)

	// The reservation must be released: a new create for the same key succeeds.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, created, err := s.Create(context.Background(), "k", build(rec("new", "newmask")))
		if err == nil && created {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservation not released: created=%v err=%v", created, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCreateWaitTimeout verifies that a waiter gives up after CreateWait.
func TestCreateWaitTimeout(t *testing.T) {
	limits := testLimits()
	limits.CreateWait = 30 * time.Millisecond
	s := NewMemory(limits)
	release := make(chan struct{})
	creatorDone := make(chan struct{})
	go func() {
		defer close(creatorDone)
		_, _, _ = s.Create(context.Background(), "k", func(context.Context) (Record, error) {
			<-release
			return rec("orig", "mask"), nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	_, _, err := s.Create(context.Background(), "k", build(rec("x", "y")))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(release)
	<-creatorDone
}

func TestCapacityEntries(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour})
	_, _, _ = s.Create(context.Background(), "a", build(rec("x", "y")))
	_, _, err := s.Create(context.Background(), "b", build(rec("x", "y")))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

func TestCapacityBytes(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 10, MaxRecordBytes: 1 << 20, TTL: time.Hour})
	_, _, _ = s.Create(context.Background(), "a", build(rec("12345", "67890")))
	_, _, err := s.Create(context.Background(), "b", build(rec("12345", "67890")))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

func TestCapacityRecordBytes(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 10, TTL: time.Hour})
	_, _, err := s.Create(context.Background(), "a", build(rec("12345", "67890")))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

// TestCapacityReleasedAfterCleanup verifies that after expired records are
// evicted, the store accepts new records again.
func TestCapacityReleasedAfterCleanup(t *testing.T) {
	now := time.Now()
	s := NewMemory(Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = func() time.Time { return now }
	_, _, _ = s.Create(context.Background(), "a", build(rec("x", "y")))
	if _, _, err := s.Create(context.Background(), "b", build(rec("x", "y"))); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected capacity error, got %v", err)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	_, created, err := s.Create(context.Background(), "b", build(rec("x", "y")))
	if err != nil || !created {
		t.Fatalf("create after cleanup = created %v, err %v", created, err)
	}
}

func TestTTLExpiry(t *testing.T) {
	now := time.Now()
	s := NewMemory(testLimits())
	s.limits.TTL = time.Minute
	s.now = func() time.Time { return now }
	_, _, _ = s.Create(context.Background(), "k", build(rec("orig", "mask")))
	if _, ok := s.Get("k"); !ok {
		t.Fatal("record should be present before TTL")
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.Get("k"); ok {
		t.Fatal("record should be expired after TTL")
	}
	// Expired record must not block a new create for the same key.
	_, created, err := s.Create(context.Background(), "k", build(rec("new", "newmask")))
	if err != nil || !created {
		t.Fatalf("Create after expiry = created %v, err %v", created, err)
	}
}

// fakeClock is a controllable clock for TTL tests. It is safe for concurrent
// use because the store calls now() from multiple goroutines.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestCleanupRaceAcrossTTL runs the background cleanup concurrently with reads
// and creates while the clock crosses the TTL boundary, so records actually
// expire under concurrency. It catches data races under -race and verifies that
// the store keeps working across the expiry boundary.
func TestCleanupRaceAcrossTTL(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewMemory(Limits{MaxEntries: 1000, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: 10 * time.Millisecond, CreateWait: time.Second, CleanupInterval: time.Millisecond})
	s.now = clock.now
	s.StartCleanup()
	defer s.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				key := string(rune('a'+i)) + string(rune('0'+j%10))
				_, _, _ = s.Create(context.Background(), key, build(rec("orig", "mask")))
				_, _ = s.Get(key)
			}
		}(i)
	}
	// Advance the clock past the TTL while the goroutines run so records expire
	// and are reclaimed concurrently with reads and creates.
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Millisecond)
		clock.advance(5 * time.Millisecond)
	}
	wg.Wait()
}

// TestDifferentKeysIndependent verifies that keys in different scopes (the
// "consumer + payload_id" dimension) never collide.
func TestDifferentKeysIndependent(t *testing.T) {
	s := NewMemory(testLimits())
	_, _, _ = s.Create(context.Background(), "scope1:id", build(rec("orig1", "mask1")))
	_, _, _ = s.Create(context.Background(), "scope2:id", build(rec("orig2", "mask2")))
	r1, _ := s.Get("scope1:id")
	r2, _ := s.Get("scope2:id")
	if r1.Masked != "mask1" || r2.Masked != "mask2" {
		t.Fatalf("scopes collided: %+v %+v", r1, r2)
	}
}

// TestLongKeyCounted verifies that the held key is included in the memory
// accounting, so a payload_id that dominates the request body cannot be stored
// for free. A single 1 MiB key with an empty record must be rejected by the
// per-record byte limit.
func TestLongKeyCounted(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour})
	key := strings.Repeat("k", 1<<20)
	if _, _, err := s.Create(context.Background(), key, build(rec("", ""))); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Create with 1 MiB key = err %v, want ErrCapacity", err)
	}
	if s.bytes != 0 {
		t.Fatalf("bytes = %d, want 0 after rejected create", s.bytes)
	}
}

// TestKeyCountedAgainstTotalBytes verifies that keys count toward the total
// byte budget, not only the per-record limit.
func TestKeyCountedAgainstTotalBytes(t *testing.T) {
	// Each record is 128 bytes of overhead plus a 100-byte key. Two records
	// need 2*(128+100)=456 bytes; a budget of 400 must reject the second.
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 400, MaxRecordBytes: 1 << 20, TTL: time.Hour})
	key := strings.Repeat("k", 100)
	if _, created, err := s.Create(context.Background(), key, build(rec("", ""))); err != nil || !created {
		t.Fatalf("first Create = created %v, err %v", created, err)
	}
	if _, _, err := s.Create(context.Background(), key+"x", build(rec("", ""))); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second Create = err %v, want ErrCapacity", err)
	}
}

// TestConcurrentFillRespectsLimit verifies that many concurrent creators for
// different keys cannot collectively exceed the byte budget: the publish step
// is atomic under the mutex and rejects any record that would overflow.
func TestConcurrentFillRespectsLimit(t *testing.T) {
	const perRecord = 100
	s := NewMemory(Limits{MaxEntries: 1000, MaxBytes: 10 * perRecord, MaxRecordBytes: 1 << 20, TTL: time.Hour})
	const n = 100
	var wg sync.WaitGroup
	created := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i)
			_, c, err := s.Create(context.Background(), key, build(rec(strings.Repeat("o", perRecord), strings.Repeat("m", perRecord))))
			if err == nil {
				created[i] = c
			}
		}(i)
	}
	wg.Wait()
	total := 0
	for _, c := range created {
		if c {
			total++
		}
	}
	if total == 0 {
		t.Fatal("no records created")
	}
	if s.bytes > 10*perRecord {
		t.Fatalf("bytes = %d, exceeded budget %d", s.bytes, 10*perRecord)
	}
	if total > 10 {
		t.Fatalf("created %d records, want at most 10 under the byte budget", total)
	}
}

// TestTTLExpiryViaHeap verifies that expired records are reclaimed by the
// expiry heap (background drain) and that a full store holding only expired
// records accepts new ones.
func TestTTLExpiryViaHeap(t *testing.T) {
	now := time.Now()
	s := NewMemory(Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = func() time.Time { return now }
	if _, created, err := s.Create(context.Background(), "a", build(rec("x", "y"))); err != nil || !created {
		t.Fatalf("Create a = created %v, err %v", created, err)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	// The store is at MaxEntries=1 and the only record is expired; a new create
	// must drain it and succeed.
	if _, created, err := s.Create(context.Background(), "b", build(rec("x", "y"))); err != nil || !created {
		t.Fatalf("Create b after expiry = created %v, err %v", created, err)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("expired record a still present")
	}
}

// TestRepeatAfterLostResponse verifies that a record is not deleted after the
// first restore, so a client that lost the response can repeat and get the same
// original again.
func TestRepeatAfterLostResponse(t *testing.T) {
	s := NewMemory(testLimits())
	if _, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	for i := 0; i < 3; i++ {
		r, ok := s.Get("k")
		if !ok || r.Original != "orig" {
			t.Fatalf("repeat %d: got %+v, %v", i, r, ok)
		}
	}
}

// TestCounterSymmetric verifies that the accounted bytes return to zero after a
// record is added and then evicted, and that the observer sees symmetric
// deltas.
func TestCounterSymmetric(t *testing.T) {
	obs := newRecordingObserver()
	now := time.Now()
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = func() time.Time { return now }
	s.SetObserver(obs)
	if _, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	if s.bytes <= 0 {
		t.Fatalf("bytes = %d after create, want positive", s.bytes)
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.Get("k"); ok {
		t.Fatal("record should be expired")
	}
	if s.bytes != 0 {
		t.Fatalf("bytes = %d after eviction, want 0", s.bytes)
	}
	_, _, _, bytes, _ := obs.snapshot()
	if bytes != 0 {
		t.Fatalf("observer bytes = %d, want 0", bytes)
	}
}

// TestRecordSizeCountsMarkersNotValues verifies that the range-based replacement
// table counts only markers and overhead, not the original entity values, so a
// record with many entities does not duplicate the original text in the table.
func TestRecordSizeCountsMarkersNotValues(t *testing.T) {
	original := strings.Repeat("email a.b@example.test; ", 100)
	rec := Record{Original: original, Masked: original, Table: []Replacement{
		{Marker: "[PII_0]", Start: 0, End: 20},
		{Marker: "[PII_1]", Start: 22, End: 42},
	}}
	// The table entries must not add the entity value bytes (20 each); only the
	// markers and fixed overhead are counted.
	size := recordSize("k", rec)
	base := int64(recordOverhead + len("k") + len(original) + len(original))
	want := base + 2*int64(replacementOverhead+len("[PII_0]"))
	if size != want {
		t.Fatalf("recordSize = %d, want %d", size, want)
	}
}

// TestExactTTLBoundaryExpires verifies that a record whose deadline equals the
// current time is expired: the single expiry boundary counts exact equality.
func TestExactTTLBoundaryExpires(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = clock.now
	if _, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	// Advance exactly to the deadline: now == createdAt + TTL.
	clock.advance(time.Minute)
	if _, ok := s.Get("k"); ok {
		t.Fatal("record should be expired at exact TTL boundary")
	}
}

// TestMassExpiryBatched verifies that a large backlog of expired records is
// reclaimed in bounded batches: a single drain pass removes at most
// drainBatchSize records, and repeated drains (as the background cleanup does)
// reclaim the whole backlog.
func TestMassExpiryBatched(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	const n = 3 * drainBatchSize
	s := NewMemory(Limits{MaxEntries: n + 1, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = clock.now
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%d", i)
		if _, created, err := s.Create(context.Background(), key, build(rec("orig", "mask"))); err != nil || !created {
			t.Fatalf("seed create %d = created %v, err %v", i, created, err)
		}
	}
	clock.advance(2 * time.Minute)

	// A single drain pass is bounded by drainBatchSize.
	s.mu.Lock()
	ev := s.drainExpired()
	s.mu.Unlock()
	if ev.removed != drainBatchSize {
		t.Fatalf("single drain removed %d, want %d", ev.removed, drainBatchSize)
	}
	if len(s.entries) != n-drainBatchSize {
		t.Fatalf("entries = %d after one batch, want %d (backlog not fully drained)", len(s.entries), n-drainBatchSize)
	}

	// Repeated drains reclaim the whole backlog.
	s.drainAll()
	if len(s.entries) != 0 {
		t.Fatalf("entries = %d after drainAll, want 0", len(s.entries))
	}
	if s.bytes != 0 {
		t.Fatalf("bytes = %d after drainAll, want 0", s.bytes)
	}
	if s.expiries.Len() != 0 {
		t.Fatalf("expiry queue len = %d after drainAll, want 0", s.expiries.Len())
	}
}

// TestCreateAtCapacityDrainsBoundedBatch verifies that a Create at capacity
// frees only a bounded number of expired records (not the whole backlog) and
// that the store accepts new records again once expired records are reclaimed.
func TestCreateAtCapacityDrainsBoundedBatch(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	const n = 3 * drainBatchSize
	s := NewMemory(Limits{MaxEntries: n, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = clock.now
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%d", i)
		if _, created, err := s.Create(context.Background(), key, build(rec("orig", "mask"))); err != nil || !created {
			t.Fatalf("seed create %d = created %v, err %v", i, created, err)
		}
	}
	clock.advance(2 * time.Minute)

	// The store is full of expired records. A Create drains a bounded number of
	// batches (one before the capacity check and one before publish) and then
	// accepts the new record; the rest of the backlog is left for cleanup.
	if _, created, err := s.Create(context.Background(), "new", build(rec("o", "m"))); err != nil || !created {
		t.Fatalf("Create after expiry = created %v, err %v", created, err)
	}
	if _, ok := s.Get("new"); !ok {
		t.Fatal("new record not present")
	}
	// The backlog is not fully drained by one Create: at most two batches were
	// reclaimed, so the remaining live records are n - 2*batch + 1.
	if want := n - 2*drainBatchSize + 1; len(s.entries) != want {
		t.Fatalf("entries = %d, want %d (only bounded batches drained)", len(s.entries), want)
	}
}

// TestRecreateKeyOldNodeDoesNotDeleteNew verifies that re-creating a key after
// its TTL does not let an old expiry node delete the new live record.
func TestRecreateKeyOldNodeDoesNotDeleteNew(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = clock.now
	if _, created, err := s.Create(context.Background(), "k", build(rec("old", "oldmask"))); err != nil || !created {
		t.Fatalf("first Create = created %v, err %v", created, err)
	}
	clock.advance(2 * time.Minute)
	if _, created, err := s.Create(context.Background(), "k", build(rec("new", "newmask"))); err != nil || !created {
		t.Fatalf("re-create = created %v, err %v", created, err)
	}
	// The new record must survive a drain that runs after its creation.
	s.drainAll()
	r, ok := s.Get("k")
	if !ok || r.Original != "new" {
		t.Fatalf("Get after drain = %+v, %v; want new record", r, ok)
	}
	// The expiry queue holds exactly one node for the live record.
	if s.expiries.Len() != 1 {
		t.Fatalf("expiry queue len = %d, want 1", s.expiries.Len())
	}
}

// TestStopIdempotent verifies that Stop is safe to call repeatedly and that the
// background goroutine actually terminates.
func TestStopIdempotent(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute, CleanupInterval: time.Millisecond})
	s.StartCleanup()
	s.Stop()
	s.Stop() // must not panic on double close
	// StartCleanup after Stop must work and Stop again must be safe.
	s.StartCleanup()
	s.Stop()
}

// TestObserverNotCalledUnderMutex verifies that the observer is invoked without
// the store mutex held during eviction. The observer records whether it holds
// the store mutex; the store must never call it while locked.
func TestObserverNotCalledUnderMutex(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Minute})
	s.now = clock.now
	obs := &mutexCheckingObserver{store: s}
	s.SetObserver(obs)
	if _, created, err := s.Create(context.Background(), "k", build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	clock.advance(2 * time.Minute)
	if _, ok := s.Get("k"); ok {
		t.Fatal("record should be expired")
	}
	if obs.locked {
		t.Fatal("observer was called while the store mutex was held")
	}
}

// mutexCheckingObserver fails if any observer method is called while the store
// mutex is held. TryLock reports whether the mutex is currently held by another
// goroutine.
type mutexCheckingObserver struct {
	store  *Memory
	locked bool
}

func (o *mutexCheckingObserver) check() {
	if !o.store.mu.TryLock() {
		o.locked = true
		return
	}
	o.store.mu.Unlock()
}
func (o *mutexCheckingObserver) RecordAdded()   { o.check() }
func (o *mutexCheckingObserver) RecordRemoved() { o.check() }
func (o *mutexCheckingObserver) BytesDelta(int64) {
	o.check()
}
func (o *mutexCheckingObserver) TTLExpired() { o.check() }
func (o *mutexCheckingObserver) Failure(string) {
	o.check()
}

// TestLongKeyReleasedAfterEviction verifies that after a record with a long key
// is evicted and the expiry queue is drained, no reference to the key remains in
// the queue's backing array. This is the diagnostic scenario: entries, bytes and
// queue length are zero, and the backing array must not retain the key.
func TestLongKeyReleasedAfterEviction(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 30, TTL: time.Minute})
	s.now = clock.now
	key := strings.Repeat("k", 1<<20)
	if _, created, err := s.Create(context.Background(), key, build(rec("orig", "mask"))); err != nil || !created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	clock.advance(2 * time.Minute)
	if _, ok := s.Get(key); ok {
		t.Fatal("record should be expired")
	}
	if len(s.entries) != 0 || s.bytes != 0 || s.expiries.Len() != 0 {
		t.Fatalf("entries=%d bytes=%d queue=%d, want all zero", len(s.entries), s.bytes, s.expiries.Len())
	}
	// The backing array slot must be zeroed so the long key is not retained.
	for _, n := range s.expiries.nodes {
		if n.key != "" {
			t.Fatal("expiry queue backing array still holds a key reference")
		}
	}
}

// TestConcurrentGetCreateAcrossTTL runs concurrent Get and Create for many keys
// while the clock crosses the TTL boundary, verifying that the store stays
// consistent and the expiry queue never accumulates stale nodes.
func TestConcurrentGetCreateAcrossTTL(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	const n = 200
	s := NewMemory(Limits{MaxEntries: n + 10, MaxBytes: 1 << 30, MaxRecordBytes: 1 << 20, TTL: 10 * time.Millisecond})
	s.now = clock.now

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < n; j++ {
				key := fmt.Sprintf("k%d", j)
				_, _, _ = s.Create(context.Background(), key, build(rec("orig", "mask")))
				_, _ = s.Get(key)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Millisecond)
		clock.advance(5 * time.Millisecond)
	}
	wg.Wait()
	// The expiry queue must hold at most one node per live record.
	if s.expiries.Len() > len(s.entries) {
		t.Fatalf("expiry queue len %d exceeds live records %d", s.expiries.Len(), len(s.entries))
	}
}
