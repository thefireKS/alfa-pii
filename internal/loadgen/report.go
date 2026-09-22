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
	// Duration is the measured run duration.
	Duration time.Duration
	// TargetRPS is the configured target rate.
	TargetRPS float64
	// Granted, Consumed, Skipped are pacer slot counts.
	Granted, Consumed, Skipped int64
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
	// StopReason is why the run stopped.
	StopReason string
	// Stats holds latency and outcome counters.
	Stats *Stats
	// Pending is the number of pending pairs at the end.
	Pending int
	// StoreRecords, StoreBytes are the final store metrics.
	StoreRecords int64
	StoreBytes   int64
	// CPUPercent, RSSBytes are the peak service resource usage.
	CPUPercent float64
	RSSBytes   int64
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
	w("Duration: %s", r.Duration.Round(time.Millisecond))
	w("Target RPS: %.1f", r.TargetRPS)
	w("Pacer: granted=%d consumed=%d skipped=%d", r.Granted, r.Consumed, r.Skipped)
	if r.Late > 0 {
		w("Late sends: %d", r.Late)
	}
	w("Sent: %d", r.Sent)
	w("Successful: mask=%d restore=%d", r.MaskSuccess, r.RestoreSuccess)
	w("Restore mismatches: %d", r.RestoreMismatch)
	w("Mask no-change (no PII): %d", r.MaskNoChange)
	w("Pending pairs at end: %d", r.Pending)
	w("Stop reason: %s", r.StopReason)
	w("")

	if r.Stats != nil {
		mean, pcts := r.Stats.Percentiles(50, 95, 99)
		w("Latency: mean=%s p50=%s p95=%s p99=%s", mean.Round(time.Microsecond),
			pcts[50].Round(time.Microsecond), pcts[95].Round(time.Microsecond), pcts[99].Round(time.Microsecond))
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

	if r.StoreRecords > 0 || r.StoreBytes > 0 {
		w("Store: records=%d bytes=%d", r.StoreRecords, r.StoreBytes)
	}
	if r.CPUPercent > 0 || r.RSSBytes > 0 {
		w("Service resources: CPU=%.1f%% RSS=%d bytes", r.CPUPercent, r.RSSBytes)
	}
	if r.ContainerLimits != "" {
		w("Container limits: %s", r.ContainerLimits)
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