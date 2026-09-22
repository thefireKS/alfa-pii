package store

import (
	"context"
	"errors"
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

// TestCleanupRaceWithRead runs the background cleanup concurrently with reads
// and creates to catch data races under -race.
func TestCleanupRaceWithRead(t *testing.T) {
	now := time.Now()
	s := NewMemory(Limits{MaxEntries: 1000, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: 10 * time.Millisecond, CreateWait: time.Second, CleanupInterval: time.Millisecond})
	s.now = func() time.Time { return now }
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
