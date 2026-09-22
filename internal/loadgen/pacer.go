package loadgen

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Pacer emits send slots at a target rate with a ramp-up and a bounded queue.
// Each slot represents one intended request. Workers consume slots; when the
// bounded queue is full, the pacer drops the slot and counts it as a generator
// skip. This separates the target rate from the achieved rate and makes skips
// visible. The pacer never blocks, so the target rate is always maintained even
// when workers are busy waiting for responses.
type Pacer struct {
	// target is the steady-state rate in requests per second.
	target float64
	// peak is the burst rate in requests per second.
	peak float64
	// burstEvery is the interval between bursts.
	burstEvery time.Duration
	// burstDuration is the duration of each burst.
	burstDuration time.Duration
	// ramp is the duration over which the rate ramps up to target.
	ramp time.Duration
	// slotCh is the bounded channel of pending send slots.
	slotCh chan struct{}
	// granted counts slots emitted by the pacer.
	granted int64
	// skipped counts slots dropped because the queue was full.
	skipped int64
	// consumed counts slots actually taken by workers.
	consumed int64
	// start is the wall-clock start of the run.
	start time.Time
	// done signals the pacer goroutine to stop.
	done chan struct{}
	// wg waits for the pacer goroutine.
	wg sync.WaitGroup
}

// PacerConfig configures the pacer.
type PacerConfig struct {
	// Target is the steady-state rate in requests per second.
	Target float64
	// Peak is the burst rate in requests per second. When zero, no bursts are
	// emitted and the rate holds at Target after the ramp.
	Peak float64
	// BurstEvery is the interval between bursts.
	BurstEvery time.Duration
	// BurstDuration is the duration of each burst.
	BurstDuration time.Duration
	// Ramp is the duration over which the rate ramps up to target.
	Ramp time.Duration
	// Queue is the maximum number of pending slots (bounded queue).
	Queue int
}

// NewPacer builds a pacer and starts its goroutine.
func NewPacer(cfg PacerConfig) *Pacer {
	if cfg.Target <= 0 {
		cfg.Target = 1
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 1
	}
	p := &Pacer{
		target:        cfg.Target,
		peak:          cfg.Peak,
		burstEvery:    cfg.BurstEvery,
		burstDuration: cfg.BurstDuration,
		ramp:          cfg.Ramp,
		slotCh:        make(chan struct{}, cfg.Queue),
		start:         time.Now(),
		done:          make(chan struct{}),
	}
	p.wg.Add(1)
	go p.run()
	return p
}

// run emits slots at the current rate using a fractional accumulator. The rate
// ramps from a low fraction to the target over the ramp period, then holds.
func (p *Pacer) run() {
	defer p.wg.Done()
	const tick = time.Millisecond
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var acc float64
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			elapsed := time.Since(p.start)
			rate := p.currentRate(elapsed)
			// Slots per tick = rate * tickSeconds.
			acc += rate * tick.Seconds()
			for acc >= 1 {
				acc--
				p.emit()
			}
		}
	}
}

// currentRate returns the rate in effect at the given elapsed time. The rate
// ramps from 10% to 100% of target over the ramp period, then holds at target
// with periodic bursts to peak when a burst schedule is configured.
func (p *Pacer) currentRate(elapsed time.Duration) float64 {
	if p.ramp > 0 && elapsed < p.ramp {
		f := float64(elapsed) / float64(p.ramp)
		// Ramp from 10% to 100% of target.
		return p.target * (0.1 + 0.9*f)
	}
	if p.peak > p.target && p.burstEvery > 0 && p.burstDuration > 0 {
		cycle := elapsed % p.burstEvery
		if cycle < p.burstDuration {
			return p.peak
		}
	}
	return p.target
}

// emit places one slot into the queue, or drops it and counts a skip when the
// queue is full.
func (p *Pacer) emit() {
	atomic.AddInt64(&p.granted, 1)
	select {
	case p.slotCh <- struct{}{}:
	default:
		atomic.AddInt64(&p.skipped, 1)
	}
}

// Wait blocks until a slot is available or the context is done. It returns
// false if the context is cancelled before a slot is granted.
func (p *Pacer) Wait(ctx context.Context) bool {
	select {
	case <-p.slotCh:
		atomic.AddInt64(&p.consumed, 1)
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop stops the pacer goroutine and waits for it to finish.
func (p *Pacer) Stop() {
	close(p.done)
	p.wg.Wait()
}

// Counts returns the granted, consumed and skipped slot counts.
func (p *Pacer) Counts() (granted, consumed, skipped int64) {
	return atomic.LoadInt64(&p.granted), atomic.LoadInt64(&p.consumed), atomic.LoadInt64(&p.skipped)
}