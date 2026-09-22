package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
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
// reason.
func (r *Runner) Run(ctx context.Context) string {
	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx)
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
// it, records the result and feeds it back to the scenario.
func (r *Runner) worker(ctx context.Context) {
	for {
		if !r.pacer.Wait(ctx) {
			return
		}
		atomic.AddInt64(&r.sent, 1)
		step, ok := r.scenario.Next()
		if !ok {
			return
		}
		resp, attempts := r.client.SendWithRetry(ctx, step.PayloadID, step.Payload, r.policy)
		r.record(step, resp, attempts)
		r.scenario.Complete(step, resp)
		if r.checkStop(resp) {
			return
		}
	}
}

// record classifies the response and updates counters. The client returns a
// neutral OutcomeOK for any HTTP 200 with a valid string result; the runner
// classifies it as a mask or restore based on the step type.
func (r *Runner) record(step Step, resp Response, attempts int) {
	payloadBytes := len(step.Payload)
	payloadChars := runeCount(step.Payload)
	switch {
	case resp.Status == 200 && resp.Outcome == OutcomeOK:
		if step.IsMask {
			if resp.Result == step.Original {
				// No PII recognized: the mask equals the original. This is not
				// a successful mask operation.
				atomic.AddInt64(&r.maskNoChange, 1)
				r.stats.RecordChars(OutcomeRepeat, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&r.maskSuccess, 1)
				r.stats.RecordChars(OutcomeMask, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		} else {
			if resp.Result == step.Original {
				atomic.AddInt64(&r.restoreSuccess, 1)
				r.stats.RecordChars(OutcomeRestore, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&r.restoreMismatch, 1)
				r.stats.RecordChars(OutcomeError, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		}
	default:
		r.stats.RecordChars(resp.Outcome, resp.Status, resp.Latency, payloadBytes, payloadChars)
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