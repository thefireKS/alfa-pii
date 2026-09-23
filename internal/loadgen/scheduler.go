package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Scheduler sends requests on a fixed schedule with a bounded queue. It is a
// separate mode from the main profile: the target rate is fixed, the queue is
// bounded, and late sends and skips are visible. Results are kept separate from
// the main profile.
type Scheduler struct {
	client   *Client
	scenario Scenario
	policy   RetryPolicy
	stats    *Stats

	// target is the fixed send rate in requests per second.
	target float64
	// queue is the bounded channel of pending steps.
	queue chan Step
	// workers is the concurrency limit.
	workers int
	// retryPacer gates retry attempts so the achieved HTTP send rate
	// (including retries) stays within the configured intensity.
	retryPacer *Pacer

	// scheduled counts steps placed on the schedule.
	scheduled int64
	// sent counts HTTP requests actually sent, including retries.
	sent int64
	// late counts steps sent later than their scheduled slot.
	late int64
	// skipped counts steps dropped because the queue was full.
	skipped int64

	// maskSuccess and restoreSuccess count verified successes.
	maskSuccess     int64
	restoreSuccess  int64
	restoreMismatch int64
	maskNoChange    int64

	// consecutiveInvalid counts consecutive invalid responses for the
	// compatibility check. 429 does not increment or reset it; success resets
	// it.
	consecutiveInvalid int64
	// maxConsecutiveInvalid is the threshold for the compatibility check.
	maxConsecutiveInvalid int
	// cancel cancels the run context when the compatibility check triggers, so
	// every worker and the producer observe the stop together.
	cancel context.CancelFunc

	stopReason atomic.Value
}

// SchedulerConfig configures the Scheduler.
type SchedulerConfig struct {
	// Target is the fixed send rate in requests per second.
	Target float64
	// Workers is the concurrency limit.
	Workers int
	// Queue is the maximum number of pending steps.
	Queue int
	// Retry is the retry policy.
	Retry RetryPolicy
	// MaxConsecutiveInvalid is the number of consecutive invalid responses
	// after which the scheduler stops early.
	MaxConsecutiveInvalid int
}

// NewScheduler builds a Scheduler.
func NewScheduler(client *Client, scenario Scenario, cfg SchedulerConfig) *Scheduler {
	if cfg.Queue <= 0 {
		cfg.Queue = 1
	}
	if cfg.Target <= 0 {
		cfg.Target = 1
	}
	if cfg.MaxConsecutiveInvalid <= 0 {
		cfg.MaxConsecutiveInvalid = 5
	}
	return &Scheduler{
		client:                client,
		scenario:              scenario,
		policy:                cfg.Retry,
		stats:                 NewStats(),
		target:                cfg.Target,
		queue:                 make(chan Step, cfg.Queue),
		workers:               cfg.Workers,
		retryPacer:            NewPacer(PacerConfig{Target: cfg.Target, Ramp: 0, Queue: cfg.Queue}),
		maxConsecutiveInvalid: cfg.MaxConsecutiveInvalid,
	}
}

// Run drives the scheduler until the context is done, the scenario is
// exhausted, or the compatibility check stops it early. It returns the stop
// reason. When the scenario is exhausted the queue is closed so workers drain
// the remaining steps and exit without waiting for the overall timeout. When
// the compatibility check triggers, the run context is cancelled so every
// worker and the producer stop together.
func (s *Scheduler) Run(ctx context.Context) string {
	defer s.retryPacer.Stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(runCtx)
		}()
	}
	s.produce(runCtx)
	// Signal workers to drain the remaining queue and exit. Closing is safe:
	// produce is the only writer, workers only read.
	close(s.queue)
	wg.Wait()
	if reason, ok := s.stopReason.Load().(string); ok && reason != "" {
		return reason
	}
	if ctx.Err() != nil {
		return "context done: " + ctx.Err().Error()
	}
	return "scenario exhausted"
}

// produce places steps on the schedule at the fixed target rate. When the
// queue is full, the step is dropped and counted as a skip. A step that waits
// in the queue past its slot is counted as late when it is sent.
func (s *Scheduler) produce(ctx context.Context) {
	const tick = time.Millisecond
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var acc float64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			acc += s.target * tick.Seconds()
			for acc >= 1 {
				acc--
				step, ok := s.scenario.Next()
				if !ok {
					return
				}
				atomic.AddInt64(&s.scheduled, 1)
				select {
				case s.queue <- step:
				default:
					atomic.AddInt64(&s.skipped, 1)
				}
			}
		}
	}
}

// worker consumes steps from the queue and sends them with retries, gating each
// retry attempt with the retry pacer so the achieved send rate stays within the
// configured intensity.
func (s *Scheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case step, ok := <-s.queue:
			if !ok {
				return
			}
			final, attempts, opLatency := s.sendWithRetry(ctx, step)
			s.record(step, final, attempts, opLatency)
			s.scenario.Complete(step, final)
			if s.checkStop(final) {
				// Cancel the run context so every worker and the producer stop
				// together, not just this worker.
				s.cancel()
				return
			}
		}
	}
}

// checkStop implements the compatibility check: stop after five consecutive
// invalid responses. A 429 does not increment or reset the counter; a success
// resets it.
func (s *Scheduler) checkStop(resp Response) bool {
	switch {
	case resp.Status == 429:
		// 429 neither increments nor resets the counter.
		return false
	case resp.IsValidSuccess():
		atomic.StoreInt64(&s.consecutiveInvalid, 0)
		return false
	default:
		n := atomic.AddInt64(&s.consecutiveInvalid, 1)
		if n >= int64(s.maxConsecutiveInvalid) {
			s.stopReason.Store("five consecutive invalid responses")
			return true
		}
		return false
	}
}

// sendWithRetry sends a step with retries, gating each retry attempt with the
// retry pacer. It returns the final response, every attempt, and the total
// logical-operation duration. The sent counter is incremented on each actual
// HTTP send.
func (s *Scheduler) sendWithRetry(ctx context.Context, step Step) (Response, []Response, time.Duration) {
	start := time.Now()
	var attempts []Response
	for attempt := 1; attempt <= s.policy.MaxAttempts; attempt++ {
		if attempt > 1 {
			if !s.retryPacer.Wait(ctx) {
				break
			}
		}
		resp := s.client.Send(ctx, step.PayloadID, step.Payload)
		atomic.AddInt64(&s.sent, 1)
		attempts = append(attempts, resp)
		if resp.Outcome != OutcomeError && resp.Outcome != OutcomeOverload && resp.Outcome != OutcomeTimeout {
			return resp, attempts, time.Since(start)
		}
		if attempt == s.policy.MaxAttempts {
			return resp, attempts, time.Since(start)
		}
		delay := s.policy.BaseDelay
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
// logical-operation duration.
func (s *Scheduler) record(step Step, final Response, attempts []Response, opLatency time.Duration) {
	payloadBytes := len(step.Payload)
	payloadChars := runeCount(step.Payload)
	for _, resp := range attempts {
		s.recordAttempt(step, resp, payloadBytes, payloadChars)
	}
	s.stats.RecordOp(opLatency)
}

// recordAttempt classifies one attempt and updates counters.
func (s *Scheduler) recordAttempt(step Step, resp Response, payloadBytes, payloadChars int) {
	switch {
	case resp.Status == 200 && resp.Outcome == OutcomeOK:
		if step.IsMask {
			if resp.Result == step.Original {
				atomic.AddInt64(&s.maskNoChange, 1)
				s.stats.RecordAttempt(LatencyOther, OutcomeRepeat, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&s.maskSuccess, 1)
				s.stats.RecordAttempt(LatencyMask, OutcomeMask, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		} else {
			if resp.Result == step.Original {
				atomic.AddInt64(&s.restoreSuccess, 1)
				s.stats.RecordAttempt(LatencyRestore, OutcomeRestore, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&s.restoreMismatch, 1)
				s.stats.RecordAttempt(LatencyError, OutcomeError, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		}
	default:
		s.stats.RecordAttempt(latencyClassFor(resp), resp.Outcome, resp.Status, resp.Latency, payloadBytes, payloadChars)
	}
}

// Stats returns the collected statistics.
func (s *Scheduler) Stats() *Stats { return s.stats }

// Counts returns the scheduled, sent, late, skipped and success counts.
func (s *Scheduler) Counts() (scheduled, sent, late, skipped, maskSuccess, restoreSuccess, restoreMismatch, maskNoChange int64) {
	return atomic.LoadInt64(&s.scheduled),
		atomic.LoadInt64(&s.sent),
		atomic.LoadInt64(&s.late),
		atomic.LoadInt64(&s.skipped),
		atomic.LoadInt64(&s.maskSuccess),
		atomic.LoadInt64(&s.restoreSuccess),
		atomic.LoadInt64(&s.restoreMismatch),
		atomic.LoadInt64(&s.maskNoChange)
}
