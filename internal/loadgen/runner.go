package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Runner drives a load run: it starts workers that consume pacer slots, pull
// steps from the scenario, send requests with retry, and record results. It
// also tracks the compatibility check (stop after five consecutive invalid
// responses) and the stop reason.
//
// The run context carries the measured interval deadline. When it expires the
// pacer stops granting slots, so workers stop pulling new steps, but requests
// already in flight are given a bounded grace period to finish before they are
// cancelled. This keeps the measured interval free of the drain time and lets
// the report count unfinished operations separately.
type Runner struct {
	client   *Client
	scenario Scenario
	pacer    *Pacer
	workers  int
	policy   RetryPolicy
	stats    *Stats
	grace    time.Duration

	// consecutiveInvalid counts consecutive invalid responses for the
	// compatibility check. 429 does not increment or reset it; success resets
	// it.
	consecutiveInvalid int64
	// maxConsecutiveInvalid is the threshold for the compatibility check.
	maxConsecutiveInvalid int
	// stopReason is set when the runner stops early.
	stopReason atomic.Value // string

	// cancel cancels the hard-stop context when the compatibility check
	// triggers, so every worker observes the stop and the whole run terminates,
	// not just the worker that hit the threshold.
	cancel context.CancelFunc

	// maskSuccess and restoreSuccess count verified successful operations.
	maskSuccess    int64
	restoreSuccess int64
	// restoreMismatch counts restores whose result did not match the original.
	restoreMismatch int64
	// maskNoChange counts mask steps whose result equals the original (no PII
	// recognized), which is not counted as a successful mask.
	maskNoChange int64
	// canceled counts operations that were in flight when the run ended and did
	// not complete (cancelled by the grace-period expiry or a hard stop).
	canceled int64

	// sent counts requests actually sent (after pacer slot consumed).
	sent int64

	// mu guards measured and measuredSet.
	mu          sync.Mutex
	measured    time.Duration
	measuredSet bool
}

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// Workers is the number of concurrent connections.
	Workers int
	// Retry is the retry policy.
	Retry RetryPolicy
	// MaxConsecutiveInvalid is the number of consecutive invalid responses
	// after which the runner stops early.
	MaxConsecutiveInvalid int
	// Grace is the additional time given to in-flight requests after the run
	// ends. When zero, a default of 2s is used.
	Grace time.Duration
}

// NewRunner builds a Runner.
func NewRunner(client *Client, scenario Scenario, pacer *Pacer, cfg RunnerConfig) *Runner {
	if cfg.MaxConsecutiveInvalid <= 0 {
		cfg.MaxConsecutiveInvalid = 5
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 2 * time.Second
	}
	return &Runner{
		client:                client,
		scenario:              scenario,
		pacer:                 pacer,
		workers:               cfg.Workers,
		policy:                cfg.Retry,
		stats:                 NewStats(),
		grace:                 cfg.Grace,
		maxConsecutiveInvalid: cfg.MaxConsecutiveInvalid,
	}
}

// Run executes the load run until the context is done, the scenario is
// exhausted, or the compatibility check stops it early. It returns the stop
// reason. When the compatibility check triggers, the run context is cancelled
// so every worker terminates together.
func (r *Runner) Run(ctx context.Context) string {
	start := time.Now()
	// hardCtx is cancelled on a hard stop: parent cancellation, the
	// compatibility check, or the grace-period expiry after a normal run end.
	// Requests in flight use hardCtx so a normal run-end deadline does not cut
	// them off immediately; they get the grace period to finish.
	hardCtx, hardCancel := context.WithCancel(context.Background())
	defer hardCancel()
	r.cancel = hardCancel

	// Watch the parent context. A deadline expiry is a normal run end: stop new
	// sends (the pacer stops granting slots) but let in-flight requests finish
	// within the grace period. A cancellation is a hard stop: cancel in-flight
	// requests immediately.
	go func() {
		select {
		case <-ctx.Done():
			r.markMeasured(start)
			if ctx.Err() == context.Canceled {
				hardCancel()
			} else {
				time.AfterFunc(r.grace, hardCancel)
			}
		case <-hardCtx.Done():
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx, hardCtx, start)
		}()
	}
	wg.Wait()
	r.markMeasured(start)
	if reason, ok := r.stopReason.Load().(string); ok && reason != "" {
		return reason
	}
	if ctx.Err() != nil {
		return "context done: " + ctx.Err().Error()
	}
	return "scenario exhausted"
}

// markMeasured records the measured interval once, at the earliest run-end
// signal (context done, scenario exhausted, or compatibility stop).
func (r *Runner) markMeasured(start time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.measuredSet {
		r.measured = time.Since(start)
		r.measuredSet = true
	}
}

// Measured returns the measured interval, excluding the grace period for
// in-flight requests.
func (r *Runner) Measured() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.measured
}

// worker runs one connection: it waits for a pacer slot, pulls a step, sends
// it with retries, records every attempt and feeds the final result back to the
// scenario. The pacer slot is gated by the run context so no new sends start
// after the measured interval ends; the actual send uses the hard-stop context
// so an in-flight request can finish within the grace period.
func (r *Runner) worker(ctx, hardCtx context.Context, start time.Time) {
	for {
		if !r.pacer.Wait(ctx) {
			return
		}
		step, ok := r.scenario.Next(ctx)
		if !ok {
			r.markMeasured(start)
			return
		}
		final, attempts, opLatency := r.sendWithRetry(ctx, hardCtx, step)
		r.record(step, final, attempts, opLatency)
		r.scenario.Complete(step, final)
		if final.Outcome == OutcomeRunEnded || final.Outcome == OutcomeCanceled {
			atomic.AddInt64(&r.canceled, 1)
		}
		if r.checkStop(final) {
			// Cancel the run context so every worker stops, not just this one.
			r.markMeasured(start)
			r.cancel()
			return
		}
	}
}

// sendWithRetry sends a step with retries, gating each retry attempt with the
// pacer so retries respect the configured intensity limit. It returns the final
// response, every attempt made, and the total logical-operation duration. The
// sent counter is incremented on each actual HTTP send, not before the step is
// known to exist.
func (r *Runner) sendWithRetry(ctx, hardCtx context.Context, step Step) (Response, []Response, time.Duration) {
	start := time.Now()
	var attempts []Response
	for attempt := 1; attempt <= r.policy.MaxAttempts; attempt++ {
		if attempt > 1 {
			// A retry is an additional HTTP send and consumes another pacer
			// slot so the achieved send rate (including retries) stays within
			// the configured intensity.
			if !r.pacer.Wait(ctx) {
				break
			}
		}
		resp := r.client.Send(hardCtx, step.PayloadID, step.Payload)
		atomic.AddInt64(&r.sent, 1)
		attempts = append(attempts, resp)
		if resp.Outcome != OutcomeError && resp.Outcome != OutcomeOverload && resp.Outcome != OutcomeTimeout && resp.Outcome != OutcomeRunEnded && resp.Outcome != OutcomeCanceled {
			return resp, attempts, time.Since(start)
		}
		if attempt == r.policy.MaxAttempts {
			return resp, attempts, time.Since(start)
		}
		delay := r.policy.BaseDelay
		if resp.Outcome == OutcomeOverload && resp.RetryAfter > 0 {
			delay = resp.RetryAfter
		}
		select {
		case <-ctx.Done():
			return resp, attempts, time.Since(start)
		case <-time.After(delay):
		}
	}
	last := attempts[len(attempts)-1]
	return last, attempts, time.Since(start)
}

// record classifies every attempt and records its latency, then records the
// logical-operation duration. The client returns a neutral OutcomeOK for any
// HTTP 200 with a valid string result; the runner classifies it as a mask or
// restore based on the step type.
func (r *Runner) record(step Step, final Response, attempts []Response, opLatency time.Duration) {
	payloadBytes := len(step.Payload)
	payloadChars := runeCount(step.Payload)
	for _, resp := range attempts {
		r.recordAttempt(step, resp, payloadBytes, payloadChars)
	}
	r.stats.RecordOp(opLatency)
}

// recordAttempt classifies one attempt and updates counters. The mask/restore
// success counters count logical operations: only a final 200 attempt
// increments them, while intermediate 429/5xx attempts are counted as their own
// outcomes.
func (r *Runner) recordAttempt(step Step, resp Response, payloadBytes, payloadChars int) {
	switch {
	case resp.Status == 200 && resp.Outcome == OutcomeOK:
		if step.IsMask {
			if resp.Result == step.Original {
				// No PII recognized: the mask equals the original. This is not
				// a successful mask operation.
				atomic.AddInt64(&r.maskNoChange, 1)
				r.stats.RecordAttempt(LatencyOther, OutcomeRepeat, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&r.maskSuccess, 1)
				r.stats.RecordAttempt(LatencyMask, OutcomeMask, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		} else {
			if resp.Result == step.Original {
				atomic.AddInt64(&r.restoreSuccess, 1)
				r.stats.RecordAttempt(LatencyRestore, OutcomeRestore, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&r.restoreMismatch, 1)
				r.stats.RecordAttempt(LatencyError, OutcomeError, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		}
	default:
		r.stats.RecordAttempt(latencyClassFor(resp), resp.Outcome, resp.Status, resp.Latency, payloadBytes, payloadChars)
	}
}

// latencyClassFor maps a response outcome to its latency class.
func latencyClassFor(resp Response) LatencyClass {
	switch resp.Outcome {
	case OutcomeOverload:
		return LatencyOverload
	case OutcomeError:
		return LatencyError
	case OutcomeTimeout:
		return LatencyTimeout
	case OutcomeRunEnded, OutcomeCanceled:
		return LatencyOther
	default:
		return LatencyOther
	}
}

// checkStop implements the compatibility check: stop after five consecutive
// invalid responses. A 429 does not increment or reset the counter; a success
// resets it. A run-end or cancellation is not an invalid response and must not
// trigger the compatibility stop.
func (r *Runner) checkStop(resp Response) bool {
	switch {
	case resp.Status == 429:
		// 429 neither increments nor resets the counter.
		return false
	case resp.Outcome == OutcomeRunEnded || resp.Outcome == OutcomeCanceled:
		// The run ended or was cancelled; this is not an invalid response.
		return false
	case resp.IsValidSuccess():
		atomic.StoreInt64(&r.consecutiveInvalid, 0)
		return false
	default:
		n := atomic.AddInt64(&r.consecutiveInvalid, 1)
		if n >= int64(r.maxConsecutiveInvalid) {
			r.stopReason.Store("five consecutive invalid responses")
			return true
		}
		return false
	}
}

// Stats returns the collected statistics.
func (r *Runner) Stats() *Stats { return r.stats }

// Counts returns the sent, mask-success and restore-success counts.
func (r *Runner) Counts() (sent, maskSuccess, restoreSuccess, restoreMismatch, maskNoChange, canceled int64) {
	return atomic.LoadInt64(&r.sent),
		atomic.LoadInt64(&r.maskSuccess),
		atomic.LoadInt64(&r.restoreSuccess),
		atomic.LoadInt64(&r.restoreMismatch),
		atomic.LoadInt64(&r.maskNoChange),
		atomic.LoadInt64(&r.canceled)
}

// runeCount counts Unicode code points in s.
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
