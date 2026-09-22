// Package store keeps correspondences between originals and masks. The
// in-memory implementation enforces entry and byte limits, a time-to-live,
// and atomic creation of a single key.
package store

import (
	"errors"
	"sync"
	"time"
)

// ErrCapacity is returned when the store cannot accept a new correspondence
// because an entry or byte limit would be exceeded.
var ErrCapacity = errors.New("store capacity exceeded")

// Record is a stored correspondence for one key.
type Record struct {
	// Original is the unmasked text.
	Original string
	// Masked is the masked text.
	Masked string
	// Table maps markers to original values for restoration.
	Table []Replacement
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
	// Create atomically inserts rec for key. If key already holds a live
	// record, the existing record is returned with created=false. If the
	// store is at capacity, ErrCapacity is returned.
	Create(key string, rec Record) (Record, bool, error)
}

// Limits bound the in-memory store.
type Limits struct {
	MaxEntries int
	MaxBytes   int64
	TTL        time.Duration
}

// Memory is a concurrency-safe in-memory Store.
type Memory struct {
	mu      sync.Mutex
	entries map[string]Record
	bytes   int64
	limits  Limits
	now     func() time.Time
}

// NewMemory returns an empty in-memory store with the given limits.
func NewMemory(limits Limits) *Memory {
	return &Memory{
		entries: make(map[string]Record),
		limits:  limits,
		now:     time.Now,
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

// Create implements Store. It is atomic for a single key: concurrent calls
// for the same key yield the same winning record.
func (s *Memory) Create(key string, rec Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired()
	if existing, ok := s.entries[key]; ok {
		return existing, false, nil
	}
	rec.CreatedAt = s.now()
	size := recordSize(rec)
	if s.bytes+size > s.limits.MaxBytes || len(s.entries) >= s.limits.MaxEntries {
		return Record{}, false, ErrCapacity
	}
	s.entries[key] = rec
	s.bytes += size
	return rec, true, nil
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

// recordSize estimates the bytes a record occupies in memory.
func recordSize(rec Record) int64 {
	size := int64(len(rec.Original) + len(rec.Masked))
	for _, r := range rec.Table {
		size += int64(len(r.Marker) + len(r.Original))
	}
	return size
}
