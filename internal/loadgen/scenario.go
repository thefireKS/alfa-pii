package loadgen

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Step is one request to send. A mask step sends the original text and expects
// a mask; a restore step sends the mask and expects the original.
type Step struct {
	// PayloadID binds the pair.
	PayloadID string
	// Payload is the text to send: the original for a mask step, the mask for
	// a restore step.
	Payload string
	// IsMask reports whether this is a forward (mask) step.
	IsMask bool
	// Original is the original text, used to verify restoration.
	Original string
	// Category is the synthetic category of the payload.
	Category Category
}

// Scenario produces steps for a load run and accepts their completions. It is
// safe for concurrent use by multiple workers.
type Scenario interface {
	// Next returns the next step, or ok=false when the scenario is exhausted or
	// the context is done. It blocks until a step is ready, so a temporary
	// absence of a ready step (for example a pair waiting out its restore
	// delay) is not reported as exhaustion.
	Next(ctx context.Context) (Step, bool)
	// Complete records the result of a step so the scenario can update pair
	// state (e.g. store the mask produced by a forward step).
	Complete(Step, Response)
	// Pending returns the number of pairs awaiting a restore step, including
	// pairs whose restore step is in flight.
	Pending() int
}

// PreparedScenario is implemented by scenarios that restore a pre-created set
// of correspondences. AddPrepared records one correspondence created by the
// preparation phase together with its payload_id and restore budget.
type PreparedScenario interface {
	AddPrepared(id, original, mask string, budget int)
}

// Mode selects the operation distribution.
type Mode string

const (
	// ModeSequential processes pairs strictly forward then reverse.
	ModeSequential Mode = "sequential"
	// ModeMaskDominant issues mostly new masks with a growing set of pending
	// pairs that are occasionally restored.
	ModeMaskDominant Mode = "mask_dominant"
	// ModeRestoreDominant restores a pre-created set of correspondences.
	ModeRestoreDominant Mode = "restore_dominant"
)

// ScenarioConfig configures a scenario.
type ScenarioConfig struct {
	// Mode selects the distribution.
	Mode Mode
	// TotalPairs is the number of distinct payload_ids the scenario will use.
	TotalPairs int
	// RestoreFraction is the fraction of steps that are restores in the
	// mask-dominant mode.
	RestoreFraction float64
	// RestoreBudget is the maximum number of restore attempts per pair. It
	// bounds retries after a failed restore so a pair is not retried forever.
	RestoreBudget int
	// RestoreDelay is the pause between a completed mask and the restore step
	// becoming available. It is used to verify the storage retention policy.
	RestoreDelay time.Duration
	// Seed is the random seed for scenario decisions and text generation.
	Seed int64
	// RunID is a unique identifier for this run. It is embedded in every
	// payload_id so a repeated run against a live service does not collide with
	// correspondences created by an earlier run. It does not affect the seed, so
	// the generated texts remain reproducible.
	RunID string
}

// NewScenario builds a scenario for the given mode.
func NewScenario(cfg ScenarioConfig, gen *TextGenerator) Scenario {
	switch cfg.Mode {
	case ModeMaskDominant:
		return newMaskDominant(cfg, gen)
	case ModeRestoreDominant:
		return newRestoreDominant(cfg, gen)
	default:
		return newSequential(cfg, gen)
	}
}

// notify is a broadcast channel used to wake a blocked Next when the scenario
// state changes. Complete closes the current channel and replaces it; a blocked
// Next observes the close and re-checks the state.
type notify struct {
	ch chan struct{}
}

func newNotify() *notify {
	return &notify{ch: make(chan struct{})}
}

func (n *notify) signal() {
	close(n.ch)
	n.ch = make(chan struct{})
}

// wait blocks until the notify channel is signalled, the timeout elapses, or
// the context is done. It returns false when the context is done. A zero or
// negative timeout waits only on the notify channel and the context.
func (n *notify) wait(ctx context.Context, mu *sync.Mutex, timeout time.Duration) bool {
	ch := n.ch
	mu.Unlock()
	defer mu.Lock()
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-ch:
			return true
		case <-timer.C:
			return true
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}

// sequential processes pairs strictly forward then reverse. A forward step is
// issued for a new pair; once its mask is known (via Complete), the pair moves
// to a pending set and a restore step is issued for it. A restore step is
// issued to at most one worker at a time (in-flight marking); a successful
// restore frees the pair's data, a failed restore re-queues it within its
// restore budget.
type sequential struct {
	mu       sync.Mutex
	notify   *notify
	gen      *TextGenerator
	runID    string
	total    int
	nextPair int
	// pending holds pairs whose mask is known and await a restore step.
	pending []string
	// inFlight marks pairs whose restore step has been issued but not yet
	// completed, so one pair is never issued to two workers.
	inFlight map[string]bool
	// masks maps payload_id to its mask.
	masks map[string]string
	// originals maps payload_id to its original text.
	originals map[string]string
	// retries tracks remaining restore attempts per pair.
	retries map[string]int
	// readyAt maps payload_id to the time its restore becomes available.
	readyAt map[string]time.Time
	// restoreBudget is the max restore attempts per pair.
	restoreBudget int
	// restoreDelay is the pause between a completed mask and the restore
	// becoming available.
	restoreDelay time.Duration
	done         bool
}

func newSequential(cfg ScenarioConfig, gen *TextGenerator) *sequential {
	if cfg.RestoreBudget <= 0 {
		cfg.RestoreBudget = 1
	}
	return &sequential{
		gen:           gen,
		runID:         cfg.RunID,
		total:         cfg.TotalPairs,
		notify:        newNotify(),
		inFlight:      make(map[string]bool),
		masks:         make(map[string]string),
		originals:     make(map[string]string),
		retries:       make(map[string]int),
		readyAt:       make(map[string]time.Time),
		restoreBudget: cfg.RestoreBudget,
		restoreDelay:  cfg.RestoreDelay,
	}
}

func (s *sequential) Next(ctx context.Context) (Step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		// Prefer issuing a restore for a pending pair whose mask is known, is
		// not in flight, has budget, and has waited out the restore delay.
		for len(s.pending) > 0 {
			id := s.pending[0]
			s.pending = s.pending[1:]
			if s.inFlight[id] {
				continue
			}
			if s.retries[id] <= 0 {
				s.dropPair(id)
				continue
			}
			if s.restoreDelay > 0 && time.Since(s.readyAt[id]) < s.restoreDelay {
				// Not ready yet; put it back and wait.
				s.pending = append(s.pending, id)
				break
			}
			s.inFlight[id] = true
			s.retries[id]--
			return Step{
				PayloadID: id,
				Payload:   s.masks[id],
				IsMask:    false,
				Original:  s.originals[id],
			}, true
		}
		if s.nextPair < s.total {
			text, cat := s.gen.Next()
			id := pairID(s.runID, s.nextPair)
			s.nextPair++
			s.originals[id] = text
			s.retries[id] = s.restoreBudget
			return Step{
				PayloadID: id,
				Payload:   text,
				IsMask:    true,
				Original:  text,
				Category:  cat,
			}, true
		}
		// No pending pairs and no new pairs to create. If there are in-flight
		// restores, wait for them to complete (they free data and may re-queue a
		// failed restore). Otherwise the scenario is exhausted.
		if len(s.pending) == 0 && len(s.inFlight) == 0 {
			s.done = true
			return Step{}, false
		}
		// No ready step right now: wait for a state change (a mask completing
		// makes a restore available, a restore delay elapses, or an in-flight
		// restore completes) or the context to be done. This distinguishes a
		// temporary absence from exhaustion.
		if !s.notify.wait(ctx, &s.mu, s.waitDurationLocked()) {
			return Step{}, false
		}
	}
}

// waitDurationLocked returns how long to wait until the earliest pending pair's
// restore delay elapses, or 0 when no pending pair is waiting out a delay.
func (s *sequential) waitDurationLocked() time.Duration {
	if s.restoreDelay <= 0 {
		return 0
	}
	var earliest time.Duration
	for _, id := range s.pending {
		if s.inFlight[id] {
			continue
		}
		remaining := s.restoreDelay - time.Since(s.readyAt[id])
		if remaining <= 0 {
			return 0
		}
		if earliest == 0 || remaining < earliest {
			earliest = remaining
		}
	}
	return earliest
}

func (s *sequential) Complete(st Step, r Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if st.IsMask {
		if !r.IsValidSuccess() {
			// Mask failed after retries; free the pair's data.
			s.dropPair(st.PayloadID)
			changed = true
		} else if _, ok := s.masks[st.PayloadID]; !ok {
			s.masks[st.PayloadID] = r.Result
			s.readyAt[st.PayloadID] = time.Now()
			s.pending = append(s.pending, st.PayloadID)
			changed = true
		}
	} else {
		// Reverse step.
		if r.IsValidSuccess() && r.Result == st.Original {
			// Successful restore: free the pair's data.
			s.dropPair(st.PayloadID)
			changed = true
		} else {
			// Failed restore: unmark in-flight and re-queue for retry within
			// budget, or free the pair when the budget is exhausted.
			delete(s.inFlight, st.PayloadID)
			if s.retries[st.PayloadID] > 0 {
				s.pending = append(s.pending, st.PayloadID)
			} else {
				s.dropPair(st.PayloadID)
			}
			changed = true
		}
	}
	if changed {
		s.notify.signal()
	}
}

// dropPair frees all data held for a completed or abandoned pair so the
// generator's memory stays bounded over a long run.
func (s *sequential) dropPair(id string) {
	delete(s.masks, id)
	delete(s.originals, id)
	delete(s.inFlight, id)
	delete(s.retries, id)
	delete(s.readyAt, id)
}

func (s *sequential) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) + len(s.inFlight)
}

// maskDominant issues mostly new masks with a growing set of pending pairs.
type maskDominant struct {
	mu       sync.Mutex
	notify   *notify
	gen      *TextGenerator
	runID    string
	total    int
	nextPair int
	restore  float64
	rng      *rand.Rand
	// pending maps payload_id to its mask and original.
	pending map[string]pairState
	// pendingOrder keeps insertion order for deterministic restore selection.
	pendingOrder []string
	// inFlight marks pairs whose restore step has been issued but not yet
	// completed, so one pair is never issued to two workers.
	inFlight map[string]bool
	// retries tracks remaining restore attempts per pair.
	retries map[string]int
	// readyAt maps payload_id to the time its restore becomes available.
	readyAt map[string]time.Time
	// restoreBudget is the max restore attempts per pair.
	restoreBudget int
	// restoreDelay is the pause between a completed mask and the restore
	// becoming available.
	restoreDelay time.Duration
	done         bool
}

type pairState struct {
	id       string
	mask     string
	original string
}

func newMaskDominant(cfg ScenarioConfig, gen *TextGenerator) *maskDominant {
	if cfg.RestoreBudget <= 0 {
		cfg.RestoreBudget = 1
	}
	return &maskDominant{
		gen:           gen,
		runID:         cfg.RunID,
		total:         cfg.TotalPairs,
		restore:       cfg.RestoreFraction,
		rng:           rand.New(rand.NewSource(cfg.Seed)),
		notify:        newNotify(),
		pending:       make(map[string]pairState),
		inFlight:      make(map[string]bool),
		retries:       make(map[string]int),
		readyAt:       make(map[string]time.Time),
		restoreBudget: cfg.RestoreBudget,
		restoreDelay:  cfg.RestoreDelay,
	}
}

func (m *maskDominant) Next(ctx context.Context) (Step, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		// Prefer a restore when the pending set is non-empty and the random draw
		// falls below the restore fraction.
		if len(m.pendingOrder) > 0 && m.rng.Float64() < m.restore {
			if st, ok := m.tryRestoreLocked(); ok {
				return st, true
			}
		}
		if m.nextPair < m.total {
			text, cat := m.gen.Next()
			id := pairID(m.runID, m.nextPair)
			m.nextPair++
			return Step{
				PayloadID: id,
				Payload:   text,
				IsMask:    true,
				Original:  text,
				Category:  cat,
			}, true
		}
		// No new pairs left; fall back to restoring pending pairs.
		if st, ok := m.tryRestoreLocked(); ok {
			return st, true
		}
		// No new pairs and no restorable pending pairs. If there are in-flight
		// restores, wait for them to complete. Otherwise the scenario is
		// exhausted.
		if len(m.pendingOrder) == 0 {
			m.done = true
			return Step{}, false
		}
		if !m.notify.wait(ctx, &m.mu, m.waitDurationLocked()) {
			return Step{}, false
		}
	}
}

// waitDurationLocked returns how long to wait until the earliest pending pair's
// restore delay elapses, or 0 when no pending pair is waiting out a delay.
func (m *maskDominant) waitDurationLocked() time.Duration {
	if m.restoreDelay <= 0 {
		return 0
	}
	var earliest time.Duration
	for _, id := range m.pendingOrder {
		if m.inFlight[id] {
			continue
		}
		remaining := m.restoreDelay - time.Since(m.readyAt[id])
		if remaining <= 0 {
			return 0
		}
		if earliest == 0 || remaining < earliest {
			earliest = remaining
		}
	}
	return earliest
}

// tryRestoreLocked picks a pending pair that is not in flight, has budget, and
// has waited out the restore delay. It returns ok=false when no such pair is
// available.
func (m *maskDominant) tryRestoreLocked() (Step, bool) {
	for i := 0; i < len(m.pendingOrder); i++ {
		idx := m.rng.Intn(len(m.pendingOrder))
		id := m.pendingOrder[idx]
		if m.inFlight[id] {
			continue
		}
		if m.retries[id] <= 0 {
			m.removePending(id)
			continue
		}
		if m.restoreDelay > 0 && time.Since(m.readyAt[id]) < m.restoreDelay {
			continue
		}
		st := m.pending[id]
		m.inFlight[id] = true
		m.retries[id]--
		return Step{
			PayloadID: id,
			Payload:   st.mask,
			IsMask:    false,
			Original:  st.original,
		}, true
	}
	return Step{}, false
}

func (m *maskDominant) Complete(st Step, r Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	if st.IsMask {
		// Forward step: store the mask so the pair can be restored later. A
		// failed forward step frees the pair's data; the caller retries with
		// the same ID and text.
		if !r.IsValidSuccess() {
			m.removePending(st.PayloadID)
			changed = true
		} else if _, ok := m.pending[st.PayloadID]; !ok {
			m.pending[st.PayloadID] = pairState{id: st.PayloadID, mask: r.Result, original: st.Original}
			m.pendingOrder = append(m.pendingOrder, st.PayloadID)
			m.retries[st.PayloadID] = m.restoreBudget
			m.readyAt[st.PayloadID] = time.Now()
			changed = true
		}
	} else {
		// Reverse step: a successful restore completes the pair and frees its
		// data. On error or 429 the pair is unmarked in-flight and retried with
		// the same ID and data within its budget.
		if r.IsValidSuccess() && r.Result == st.Original {
			m.removePending(st.PayloadID)
			delete(m.inFlight, st.PayloadID)
			changed = true
		} else {
			delete(m.inFlight, st.PayloadID)
			if m.retries[st.PayloadID] <= 0 {
				m.removePending(st.PayloadID)
			}
			changed = true
		}
	}
	if changed {
		m.notify.signal()
	}
}

// removePending drops a pair from the pending set and frees its data.
func (m *maskDominant) removePending(id string) {
	if _, ok := m.pending[id]; !ok {
		return
	}
	delete(m.pending, id)
	delete(m.retries, id)
	delete(m.readyAt, id)
	for i, v := range m.pendingOrder {
		if v == id {
			m.pendingOrder = append(m.pendingOrder[:i], m.pendingOrder[i+1:]...)
			break
		}
	}
}

func (m *maskDominant) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	// In-flight pairs remain in pendingOrder until their restore completes, so
	// the length of pendingOrder counts both waiting and in-flight pairs once.
	return len(m.pendingOrder)
}

// restoreDominant restores a pre-created set of correspondences. The set is
// built by the preparation phase; each pair has a bounded restore budget so a
// successful restore is not repeated infinitely under one ID.
type restoreDominant struct {
	mu      sync.Mutex
	notify  *notify
	gen     *TextGenerator
	runID   string
	restore float64
	rng     *rand.Rand
	// pairs holds the pre-created correspondences together with their payload_id.
	pairs []pairState
	// budgets tracks remaining restores per pair index.
	budgets []int
	// nextNew is the index of the next new pair to create (for the small
	// fraction of new masks during the run).
	nextNew int
	total   int
	done    bool
}

func newRestoreDominant(cfg ScenarioConfig, gen *TextGenerator) *restoreDominant {
	return &restoreDominant{
		gen:     gen,
		runID:   cfg.RunID,
		restore: cfg.RestoreFraction,
		rng:     rand.New(rand.NewSource(cfg.Seed)),
		notify:  newNotify(),
		total:   cfg.TotalPairs,
	}
}

// AddPrepared registers a pre-created correspondence from the preparation phase
// together with the payload_id it was created under, so a restore step reuses
// the exact ID and never guesses it from the pair index.
func (r *restoreDominant) AddPrepared(id, original, mask string, budget int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pairs = append(r.pairs, pairState{id: id, mask: mask, original: original})
	r.budgets = append(r.budgets, budget)
	r.notify.signal()
}

func (r *restoreDominant) Next(ctx context.Context) (Step, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		// Prefer restoring a prepared pair with remaining budget.
		if len(r.pairs) > 0 && r.rng.Float64() < r.restore {
			if st, ok := r.tryRestoreLocked(); ok {
				return st, true
			}
		}
		// Otherwise create a new pair (mask step).
		if r.nextNew < r.total {
			text, cat := r.gen.Next()
			id := pairID(r.runID, r.total+r.nextNew)
			r.nextNew++
			return Step{
				PayloadID: id,
				Payload:   text,
				IsMask:    true,
				Original:  text,
				Category:  cat,
			}, true
		}
		// No new pairs and no budgeted restores left; fall back to any prepared
		// pair regardless of budget to keep the run going.
		if st, ok := r.tryRestoreLocked(); ok {
			return st, true
		}
		// No new pairs and no budgeted restores remain: the scenario is
		// exhausted.
		r.done = true
		return Step{}, false
	}
}

// tryRestoreLocked picks a prepared pair with remaining budget and decrements
// it. It returns ok=false when no budgeted pair is available.
func (r *restoreDominant) tryRestoreLocked() (Step, bool) {
	for i := range r.pairs {
		if r.budgets[i] > 0 {
			r.budgets[i]--
			p := r.pairs[i]
			return Step{
				PayloadID: p.id,
				Payload:   p.mask,
				IsMask:    false,
				Original:  p.original,
			}, true
		}
	}
	return Step{}, false
}

func (r *restoreDominant) Complete(st Step, resp Response) {
	// New masks created during the run are not added to the restore set; the
	// prepared set is the source of restores.
}

func (r *restoreDominant) Pending() int { return 0 }

// pairID builds a stable payload_id for a pair index. The run_id is embedded so
// a repeated run against a live service does not collide with correspondences
// created by an earlier run.
func pairID(runID string, i int) string {
	return "load-" + runID + "-" + itoa(i)
}

// prepID builds the payload_id used by the preparation phase for a prepared
// pair. It must match the ID used when the correspondence was created.
func prepID(runID string, i int) string {
	return "prep-" + runID + "-" + itoa(i)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
