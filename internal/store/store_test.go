package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func rec(original, masked string) Record {
	return Record{Original: original, Masked: masked}
}

func TestCreateAndGet(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, TTL: time.Hour})
	got, created, err := s.Create("k", rec("orig", "mask"))
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
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, TTL: time.Hour})
	_, _, _ = s.Create("k", rec("orig", "mask"))
	got, created, err := s.Create("k", rec("other", "othermask"))
	if err != nil || created {
		t.Fatalf("Create = created %v, err %v", created, err)
	}
	if got.Original != "orig" {
		t.Fatalf("got %+v, want existing record", got)
	}
}

func TestConcurrentCreateSameKey(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, TTL: time.Hour})
	const n = 50
	var wg sync.WaitGroup
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, created, _ := s.Create("k", rec("orig", "mask"))
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

func TestCapacityEntries(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 1, MaxBytes: 1 << 20, TTL: time.Hour})
	_, _, _ = s.Create("a", rec("x", "y"))
	_, _, err := s.Create("b", rec("x", "y"))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

func TestCapacityBytes(t *testing.T) {
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 10, TTL: time.Hour})
	_, _, _ = s.Create("a", rec("12345", "67890"))
	_, _, err := s.Create("b", rec("12345", "67890"))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	now := time.Now()
	s := NewMemory(Limits{MaxEntries: 10, MaxBytes: 1 << 20, TTL: time.Minute})
	s.now = func() time.Time { return now }
	_, _, _ = s.Create("k", rec("orig", "mask"))
	if _, ok := s.Get("k"); !ok {
		t.Fatal("record should be present before TTL")
	}
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.Get("k"); ok {
		t.Fatal("record should be expired after TTL")
	}
	// Expired record must not block a new create for the same key.
	_, created, err := s.Create("k", rec("new", "newmask"))
	if err != nil || !created {
		t.Fatalf("Create after expiry = created %v, err %v", created, err)
	}
}
