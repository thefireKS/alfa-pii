package loadgen

import (
	"sort"
	"sync"
	"time"
)

// LatencyClass groups per-attempt latencies so the report can separate the
// round-trip time of successful masks, successful restores, overloads, errors
// and timeouts instead of mixing them into one distribution.
type LatencyClass string

// Latency classes. The set is fixed and bounded.
const (
	LatencyMask     LatencyClass = "mask"     // successful mask attempt
	LatencyRestore  LatencyClass = "restore"  // successful restore attempt
	LatencyOverload LatencyClass = "overload" // 429
	LatencyError    LatencyClass = "error"    // 5xx or transport error
	LatencyTimeout  LatencyClass = "timeout"  // client timeout
	LatencyOther    LatencyClass = "other"    // repeat, conflict, invalid, etc.
)

// LatencySummary holds the mean and common percentiles of a latency sample.
type LatencySummary struct {
	Mean time.Duration
	P50  time.Duration
	P95  time.Duration
	P99  time.Duration
}

// Stats collects latency and outcome counters for a load run. It is safe for
// concurrent use. Per-attempt latencies are grouped by class; logical-operation
// durations (including retries) are tracked separately.
type Stats struct {
	mu sync.Mutex
	// latency holds every attempt latency for the overall distribution.
	latency []time.Duration
	// latencyByClass groups attempt latencies by class.
	latencyByClass map[LatencyClass][]time.Duration
	// opLatency holds the duration of each completed logical operation,
	// including all of its retries.
	opLatency []time.Duration
	byOutcome map[Outcome]int64
	byStatus  map[int]int64
	bytes     int64
	chars     int64
}

// NewStats returns an empty Stats.
func NewStats() *Stats {
	return &Stats{
		latencyByClass: make(map[LatencyClass][]time.Duration),
		byOutcome:      make(map[Outcome]int64),
		byStatus:       make(map[int]int64),
	}
}

// RecordAttempt adds one completed request attempt with its latency class.
func (s *Stats) RecordAttempt(class LatencyClass, outcome Outcome, status int, latency time.Duration, payloadBytes, payloadChars int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = append(s.latency, latency)
	s.latencyByClass[class] = append(s.latencyByClass[class], latency)
	s.byOutcome[outcome]++
	s.byStatus[status]++
	s.bytes += int64(payloadBytes)
	s.chars += int64(payloadChars)
}

// RecordOp adds the duration of one completed logical operation, including all
// of its retries.
func (s *Stats) RecordOp(latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opLatency = append(s.opLatency, latency)
}

// Count returns the number of recorded attempts.
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

// Percentiles returns the mean and the given percentiles of the overall attempt
// latency. pcts are values in [0,100]. The result is a map from percentile to
// duration.
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
		out[p] = sorted[percentileIndex(len(sorted), p)]
	}
	return mean, out
}

// LatencyByClass returns the latency summary for each class that has samples.
func (s *Stats) LatencyByClass() map[LatencyClass]LatencySummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[LatencyClass]LatencySummary, len(s.latencyByClass))
	for class, lats := range s.latencyByClass {
		out[class] = summarize(lats)
	}
	return out
}

// OpLatency returns the summary of logical-operation durations (with retries).
func (s *Stats) OpLatency() LatencySummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return summarize(s.opLatency)
}

// summarize computes the mean and p50/p95/p99 of a latency sample.
func summarize(lats []time.Duration) LatencySummary {
	if len(lats) == 0 {
		return LatencySummary{}
	}
	sorted := make([]time.Duration, len(lats))
	copy(sorted, lats)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return LatencySummary{
		Mean: sum / time.Duration(len(sorted)),
		P50:  sorted[percentileIndex(len(sorted), 50)],
		P95:  sorted[percentileIndex(len(sorted), 95)],
		P99:  sorted[percentileIndex(len(sorted), 99)],
	}
}

// percentileIndex returns the index of the p-th percentile in a sorted sample
// of length n.
func percentileIndex(n int, p float64) int {
	return int(float64(n-1) * p / 100.0)
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