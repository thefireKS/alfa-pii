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
	client  *Client
	scenario Scenario
	policy  RetryPolicy
	stats   *Stats

	// target is the fixed send rate in requests per second.
	target float64
	// queue is the bounded channel of pending steps.
	queue chan Step
	// workers is the concurrency limit.
	workers int

	// scheduled counts steps placed on the schedule.
	scheduled int64
	// sent counts steps actually sent.
	sent int64
	// late counts steps sent later than their scheduled slot.
	late int64
	// skipped counts steps dropped because the queue was full.
	skipped int64

	// maskSuccess and restoreSuccess count verified successes.
	maskSuccess    int64
	restoreSuccess int64
	restoreMismatch int64
	maskNoChange   int64

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
}

// NewScheduler builds a Scheduler.
func NewScheduler(client *Client, scenario Scenario, cfg SchedulerConfig) *Scheduler {
	if cfg.Queue <= 0 {
		cfg.Queue = 1
	}
	return &Scheduler{
		client:   client,
		scenario: scenario,
		policy:   cfg.Retry,
		stats:    NewStats(),
		target:   cfg.Target,
		queue:    make(chan Step, cfg.Queue),
		workers:  cfg.Workers,
	}
}

// Run drives the scheduler until the context is done or the scenario is
// exhausted. It returns the stop reason.
func (s *Scheduler) Run(ctx context.Context) string {
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	s.produce(ctx)
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

// worker consumes steps from the queue and sends them.
func (s *Scheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case step, ok := <-s.queue:
			if !ok {
				return
			}
			atomic.AddInt64(&s.sent, 1)
			resp, _ := s.client.SendWithRetry(ctx, step.PayloadID, step.Payload, s.policy)
			s.record(step, resp)
			s.scenario.Complete(step, resp)
		}
	}
}

// record classifies a response and updates counters.
func (s *Scheduler) record(step Step, resp Response) {
	payloadBytes := len(step.Payload)
	payloadChars := runeCount(step.Payload)
	switch {
	case resp.Status == 200 && resp.Outcome == OutcomeOK:
		if step.IsMask {
			if resp.Result == step.Original {
				atomic.AddInt64(&s.maskNoChange, 1)
				s.stats.RecordChars(OutcomeRepeat, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&s.maskSuccess, 1)
				s.stats.RecordChars(OutcomeMask, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		} else {
			if resp.Result == step.Original {
				atomic.AddInt64(&s.restoreSuccess, 1)
				s.stats.RecordChars(OutcomeRestore, resp.Status, resp.Latency, payloadBytes, payloadChars)
			} else {
				atomic.AddInt64(&s.restoreMismatch, 1)
				s.stats.RecordChars(OutcomeError, resp.Status, resp.Latency, payloadBytes, payloadChars)
			}
		}
	default:
		s.stats.RecordChars(resp.Outcome, resp.Status, resp.Latency, payloadBytes, payloadChars)
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