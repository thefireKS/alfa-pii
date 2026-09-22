package loadgen

import (
	"sort"
	"sync"
	"time"
)

// Stats collects latency and outcome counters for a load run. It is safe for
// concurrent use.
type Stats struct {
	mu       sync.Mutex
	latency  []time.Duration
	byOutcome map[Outcome]int64
	byStatus map[int]int64
	bytes    int64
	chars    int64
}

// NewStats returns an empty Stats.
func NewStats() *Stats {
	return &Stats{
		byOutcome: make(map[Outcome]int64),
		byStatus:  make(map[int]int64),
	}
}

// Record adds one completed request to the stats.
func (s *Stats) Record(outcome Outcome, status int, latency time.Duration, payloadBytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = append(s.latency, latency)
	s.byOutcome[outcome]++
	s.byStatus[status]++
	s.bytes += int64(payloadBytes)
	s.chars += int64(payloadBytes) // approximate; exact chars counted by caller when needed
}

// RecordChars adds a payload with an explicit character count.
func (s *Stats) RecordChars(outcome Outcome, status int, latency time.Duration, bytes, chars int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = append(s.latency, latency)
	s.byOutcome[outcome]++
	s.byStatus[status]++
	s.bytes += int64(bytes)
	s.chars += int64(chars)
}

// Count returns the number of recorded requests.
func (s *Stats) Count() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.latency))
}

// OutcomeCount returns the count for an outcome.
func (s *Stats) OutcomeCount(o Outcome) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byOutcome[o]
}

// StatusCount returns the count for an HTTP status.
func (s *Stats) StatusCount(status int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byStatus[status]
}

// Bytes returns the total payload bytes recorded.
func (s *Stats) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// Chars returns the total payload characters recorded.
func (s *Stats) Chars() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chars
}

// Percentiles returns the mean and the given percentiles of latency. pcts are
// values in [0,100]. The result is a map from percentile to duration.
func (s *Stats) Percentiles(pcts ...float64) (mean time.Duration, out map[float64]time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out = make(map[float64]time.Duration)
	if len(s.latency) == 0 {
		return 0, out
	}
	sorted := make([]time.Duration, len(s.latency))
	copy(sorted, s.latency)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	mean = sum / time.Duration(len(sorted))
	for _, p := range pcts {
		idx := int(float64(len(sorted)-1) * p / 100.0)
		out[p] = sorted[idx]
	}
	return mean, out
}

// Snapshot is a point-in-time copy of the counters for reporting.
type Snapshot struct {
	Count    int64
	Outcomes map[Outcome]int64
	Statuses map[int]int64
	Bytes    int64
	Chars    int64
}

// Snapshot returns a copy of the current counters.
func (s *Stats) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		Count:    int64(len(s.latency)),
		Outcomes: make(map[Outcome]int64, len(s.byOutcome)),
		Statuses: make(map[int]int64, len(s.byStatus)),
		Bytes:    s.bytes,
		Chars:    s.chars,
	}
	for k, v := range s.byOutcome {
		out.Outcomes[k] = v
	}
	for k, v := range s.byStatus {
		out.Statuses[k] = v
	}
	return out
}