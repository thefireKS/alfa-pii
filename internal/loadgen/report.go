package loadgen

import (
	"fmt"
	"strings"
	"time"
)

// Report is the structured result of a load run.
type Report struct {
	// Title identifies the run.
	Title string
	// Mode is the scenario mode.
	Mode Mode
	// Seed is the generator seed.
	Seed int64
	// RunID is the run identifier embedded in payload_ids.
	RunID string
	// Command is the full command line used to run the load test.
	Command string
	// Duration is the measured run duration (excluding the drain grace period).
	Duration time.Duration
	// Grace is the additional time given to in-flight requests after the run.
	Grace time.Duration
	// TargetRPS is the configured target rate.
	TargetRPS float64
	// PeakRPS is the configured burst rate.
	PeakRPS float64
	// Ramp is the configured ramp-up duration.
	Ramp time.Duration
	// BurstEvery and BurstDuration describe the burst schedule.
	BurstEvery, BurstDuration time.Duration
	// Granted, Consumed, Skipped are pacer slot counts.
	Granted, Consumed, Skipped int64
	// Overdue is the number of slots that were due but not emitted because of
	// the per-iteration burst cap.
	Overdue int64
	// Late is the number of scheduled sends that were late (scheduler mode).
	Late int64
	// Sent is the number of requests actually sent.
	Sent int64
	// MaskSuccess, RestoreSuccess are verified successful operations.
	MaskSuccess, RestoreSuccess int64
	// RestoreMismatch counts restores whose result did not match.
	RestoreMismatch int64
	// MaskNoChange counts mask steps with no recognized PII.
	MaskNoChange int64
	// Canceled counts operations that were in flight when the run ended and did
	// not complete.
	Canceled int64
	// StopReason is why the run stopped.
	StopReason string
	// Stats holds latency and outcome counters.
	Stats *Stats
	// LatencyByClass holds per-attempt latency summaries by class.
	LatencyByClass map[LatencyClass]LatencySummary
	// OpLatency holds the logical-operation latency summary (with retries).
	OpLatency LatencySummary
	// ActualRPS is the actual HTTP send rate over the measured interval,
	// counting every attempt including retries.
	ActualRPS float64
	// SuccessRPS is the successful-operation rate over the measured interval.
	SuccessRPS float64
	// OverloadFraction is the fraction of attempts that returned 429.
	OverloadFraction float64
	// Pending is the number of pending pairs at the end.
	Pending int
	// StoreRecordsPending, StoreRecordsReplay are the final store record counts
	// by phase.
	StoreRecordsPending, StoreRecordsReplay int64
	// StoreBytesPending, StoreBytesReplay are the final store byte counts by
	// phase.
	StoreBytesPending, StoreBytesReplay int64
	// StoreFailures are the store failure counters by reason.
	StoreFailures map[string]int64
	// StoreTTL is the total TTL evictions observed.
	StoreTTL int64
	// Active is the final active-request count from the service metrics.
	Active int64
	// MetricsOK reports whether the final metrics poll succeeded. When false,
	// the store/resource fields are missing data, not zero values.
	MetricsOK bool
	// CPUPercent, RSSBytes are the peak service resource usage (container).
	CPUPercent float64
	RSSBytes   int64
	// ServiceHeap is the service process Go heap in use at the end, from the
	// pii_process_heap_inuse_bytes gauge. It complements RSS: RSS can stay high
	// after objects are freed, while the heap gauge reflects live allocations.
	ServiceHeap int64
	// GeneratorHeap is the generator's own Go heap in use at the end.
	GeneratorHeap int64
	// GeneratorPeakHeap is the peak generator Go heap observed during the run.
	GeneratorPeakHeap int64
	// StoreDynamics is the sampled store fill and request counters over time.
	StoreDynamics []StoreSample
	// ContainerLimits describe the container resource limits.
	ContainerLimits string
	// SizeProfile describes the payload size distribution.
	SizeProfile string
	// OpFraction describes the operation mix.
	OpFraction string
	// Preparation describes the preparation phase (restore-dominant).
	Preparation string
	// Notes lists caveats and limitations.
	Notes []string
}

// String renders the report as text.
func (r *Report) String() string {
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	w("=== %s ===", r.Title)
	w("Mode: %s", r.Mode)
	w("Seed: %d", r.Seed)
	if r.RunID != "" {
		w("Run ID: %s", r.RunID)
	}
	if r.Command != "" {
		w("Command: %s", r.Command)
	}
	w("Duration: %s", r.Duration.Round(time.Millisecond))
	if r.Grace > 0 {
		w("Drain grace: %s", r.Grace.Round(time.Millisecond))
	}
	w("Target RPS: %.1f", r.TargetRPS)
	if r.PeakRPS > 0 {
		w("Peak RPS: %.1f ramp=%s burst_every=%s burst_duration=%s", r.PeakRPS, r.Ramp.Round(time.Millisecond), r.BurstEvery.Round(time.Millisecond), r.BurstDuration.Round(time.Millisecond))
	}
	w("Pacer: granted=%d consumed=%d skipped=%d overdue=%d", r.Granted, r.Consumed, r.Skipped, r.Overdue)
	if r.Late > 0 {
		w("Late sends: %d", r.Late)
	}
	w("Sent: %d", r.Sent)
	w("Successful: mask=%d restore=%d", r.MaskSuccess, r.RestoreSuccess)
	w("Restore mismatches: %d", r.RestoreMismatch)
	w("Mask no-change (no PII): %d", r.MaskNoChange)
	if r.Canceled > 0 {
		w("Canceled (unfinished at run end): %d", r.Canceled)
	}
	if r.ActualRPS > 0 || r.SuccessRPS > 0 {
		w("RPS: actual=%.1f success=%.1f", r.ActualRPS, r.SuccessRPS)
	}
	if r.OverloadFraction > 0 {
		w("429 fraction: %.3f", r.OverloadFraction)
	}
	w("Pending pairs at end: %d", r.Pending)
	w("Stop reason: %s", r.StopReason)
	w("")

	if r.Stats != nil {
		mean, pcts := r.Stats.Percentiles(50, 95, 99)
		w("Latency (all attempts): mean=%s p50=%s p95=%s p99=%s", mean.Round(time.Microsecond),
			pcts[50].Round(time.Microsecond), pcts[95].Round(time.Microsecond), pcts[99].Round(time.Microsecond))
		if len(r.LatencyByClass) > 0 {
			w("Latency by class:")
			for _, c := range []LatencyClass{LatencyMask, LatencyRestore, LatencyOverload, LatencyError, LatencyTimeout, LatencyOther} {
				if s, ok := r.LatencyByClass[c]; ok {
					w("  %-9s mean=%s p50=%s p95=%s p99=%s", c, s.Mean.Round(time.Microsecond),
						s.P50.Round(time.Microsecond), s.P95.Round(time.Microsecond), s.P99.Round(time.Microsecond))
				}
			}
		}
		if r.OpLatency.Mean > 0 {
			w("Logical operation latency (with retries): mean=%s p50=%s p95=%s p99=%s",
				r.OpLatency.Mean.Round(time.Microsecond), r.OpLatency.P50.Round(time.Microsecond),
				r.OpLatency.P95.Round(time.Microsecond), r.OpLatency.P99.Round(time.Microsecond))
		}
		snap := r.Stats.Snapshot()
		w("Requests recorded: %d", snap.Count)
		w("Outcomes:")
		for _, o := range []Outcome{OutcomeMask, OutcomeRestore, OutcomeRepeat, OutcomeConflict, OutcomeOverload, OutcomeError, OutcomeTimeout, OutcomeInvalid} {
			if n := snap.Outcomes[o]; n > 0 {
				w("  %-10s %d", o, n)
			}
		}
		w("Statuses:")
		for status, n := range snap.Statuses {
			w("  HTTP %d: %d", status, n)
		}
		w("Payload bytes: %d, chars: %d", snap.Bytes, snap.Chars)
	}
	w("")

	if r.MetricsOK {
		w("Store: pending records=%d bytes=%d; replay records=%d bytes=%d; ttl_expired=%d",
			r.StoreRecordsPending, r.StoreBytesPending, r.StoreRecordsReplay, r.StoreBytesReplay, r.StoreTTL)
		if len(r.StoreFailures) > 0 {
			w("Store failures:")
			for reason, n := range r.StoreFailures {
				w("  %-12s %d", reason, n)
			}
		}
		w("Active requests: %d", r.Active)
	} else {
		w("Store metrics: unavailable (poll failed)")
	}
	if r.CPUPercent > 0 || r.RSSBytes > 0 {
		w("Service resources (container): CPU=%.1f%% RSS=%d bytes", r.CPUPercent, r.RSSBytes)
	}
	if r.ServiceHeap > 0 {
		w("Service heap in use: %d bytes", r.ServiceHeap)
	}
	if r.GeneratorHeap > 0 || r.GeneratorPeakHeap > 0 {
		w("Generator resources (Go heap): heap=%d bytes peak=%d bytes", r.GeneratorHeap, r.GeneratorPeakHeap)
	}
	if r.ContainerLimits != "" {
		w("Container limits: %s", r.ContainerLimits)
	}
	if len(r.StoreDynamics) > 0 {
		w("Store dynamics (records, bytes, heap, mask, restore, 429, errors):")
		for _, s := range r.StoreDynamics {
			if !s.OK {
				w("  %s  (metrics unavailable)", s.At.Format("15:04:05"))
				continue
			}
			w("  %s  records=%d bytes=%d heap=%d mask=%d restore=%d 429=%d errors=%d",
				s.At.Format("15:04:05"), s.Records, s.Bytes, s.HeapInUse, s.Mask, s.Restore, s.Overload, s.Errors)
		}
	}
	if r.SizeProfile != "" {
		w("Size profile: %s", r.SizeProfile)
	}
	if r.OpFraction != "" {
		w("Operation mix: %s", r.OpFraction)
	}
	if r.Preparation != "" {
		w("Preparation: %s", r.Preparation)
	}
	if len(r.Notes) > 0 {
		w("Notes:")
		for _, n := range r.Notes {
			w("  - %s", n)
		}
	}
	return b.String()
}