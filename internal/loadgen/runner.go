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
type Runner struct {
	client   *Client
	scenario Scenario
	pacer    *Pacer
	workers  int
	policy   RetryPolicy
	stats    *Stats

	// consecutiveInvalid counts consecutive invalid responses for the
	// compatibility check. 429 does not increment or reset it; success resets
	// it.
	consecutiveInvalid int64
	// maxConsecutiveInvalid is the threshold for the compatibility check.
	maxConsecutiveInvalid int
	// stopReason is set when the runner stops early.
	stopReason atomic.Value // string

	// cancel cancels the run context when the compatibility check triggers, so
	// every worker observes the stop and the whole run terminates, not just the
	// worker that hit the threshold.
	cancel context.CancelFunc

	// maskSuccess and restoreSuccess count verified successful operations.
	maskSuccess    int64
	restoreSuccess int64
	// restoreMismatch counts restores whose result did not match the original.
	restoreMismatch int64
	// maskNoChange counts mask steps whose result equals the original (no PII
	// recognized), which is not counted as a successful mask.
	maskNoChange int64

	// sent counts requests actually sent (after pacer slot consumed).
	sent int64
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
}

// NewRunner builds a Runner.
func NewRunner(client *Client, scenario Scenario, pacer *Pacer, cfg RunnerConfig) *Runner {
	if cfg.MaxConsecutiveInvalid <= 0 {
		cfg.MaxConsecutiveInvalid = 5
	}
	return &Runner{
		client:                client,
		scenario:              scenario,
		pacer:                 pacer,
		workers:               cfg.Workers,
		policy:                cfg.Retry,
		stats:                 NewStats(),
		maxConsecutiveInvalid: cfg.MaxConsecutiveInvalid,
	}
}

// Run executes the load run until the context is done, the scenario is
// exhausted, or the compatibility check stops it early. It returns the stop
// reason. When the compatibility check triggers, the run context is cancelled
// so every worker terminates together.
func (r *Runner) Run(ctx context.Context) string {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.cancel = cancel
	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(runCtx)
		}()
	}
	wg.Wait()
	if reason, ok := r.stopReason.Load().(string); ok && reason != "" {
		return reason
	}
	if ctx.Err() != nil {
		return "context done: " + ctx.Err().Error()
	}
	return "scenario exhausted"
}

// worker runs one connection: it waits for a pacer slot, pulls a step, sends
// it with retries, records every attempt and feeds the final result back to the
// scenario.
func (r *Runner) worker(ctx context.Context) {
	for {
		if !r.pacer.Wait(ctx) {
			return
		}
		step, ok := r.scenario.Next()
		if !ok {
			return
		}
		final, attempts, opLatency := r.sendWithRetry(ctx, step)
		r.record(step, final, attempts, opLatency)
		r.scenario.Complete(step, final)
		if r.checkStop(final) {
			// Cancel the run context so every worker stops, not just this one.
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
func (r *Runner) sendWithRetry(ctx context.Context, step Step) (Response, []Response, time.Duration) {
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
		resp := r.client.Send(ctx, step.PayloadID, step.Payload)
		atomic.AddInt64(&r.sent, 1)
		attempts = append(attempts, resp)
		if resp.Outcome != OutcomeError && resp.Outcome != OutcomeOverload && resp.Outcome != OutcomeTimeout {
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
	default:
		return LatencyOther
	}
}

// checkStop implements the compatibility check: stop after five consecutive
// invalid responses. A 429 does not increment or reset the counter; a success
// resets it.
func (r *Runner) checkStop(resp Response) bool {
	switch {
	case resp.Status == 429:
		// 429 neither increments nor resets the counter.
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
func (r *Runner) Counts() (sent, maskSuccess, restoreSuccess, restoreMismatch, maskNoChange int64) {
	return atomic.LoadInt64(&r.sent),
		atomic.LoadInt64(&r.maskSuccess),
		atomic.LoadInt64(&r.restoreSuccess),
		atomic.LoadInt64(&r.restoreMismatch),
		atomic.LoadInt64(&r.maskNoChange)
}

// runeCount counts Unicode code points in s.
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
