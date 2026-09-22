package loadgen

import (
	"math/rand"
	"sync"
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
	// Next returns the next step, or ok=false when the scenario is exhausted.
	Next() (Step, bool)
	// Complete records the result of a step so the scenario can update pair
	// state (e.g. store the mask produced by a forward step).
	Complete(Step, Response)
	// Pending returns the number of pairs awaiting a restore step.
	Pending() int
}

// PreparedScenario is implemented by scenarios that restore a pre-created set
// of correspondences. AddPrepared records one correspondence created by the
// preparation phase together with its restore budget.
type PreparedScenario interface {
	AddPrepared(original, mask string, budget int)
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
	// RestoreBudget is the maximum number of restores per pair in the
	// restore-dominant mode.
	RestoreBudget int
	// Seed is the random seed for scenario decisions.
	Seed int64
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

// sequential processes pairs strictly forward then reverse. A forward step is
// issued for a new pair; once its mask is known (via Complete), the pair moves
// to a pending-reverse set and a restore step is issued for it. Next prefers
// issuing a restore for a pair whose mask is known, so a restore is never
// issued before its forward step has completed.
type sequential struct {
	mu       sync.Mutex
	gen      *TextGenerator
	total    int
	nextPair int
	// pendingReverse holds pairs whose mask is known and await a restore step.
	pendingReverse []string
	// masks maps payload_id to its mask.
	masks map[string]string
	// originals maps payload_id to its original text.
	originals map[string]string
	done      bool
}

func newSequential(cfg ScenarioConfig, gen *TextGenerator) *sequential {
	return &sequential{
		gen:       gen,
		total:     cfg.TotalPairs,
		masks:     make(map[string]string),
		originals: make(map[string]string),
	}
}

func (s *sequential) Next() (Step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Prefer issuing a restore for a pair whose mask is known.
	if len(s.pendingReverse) > 0 {
		id := s.pendingReverse[0]
		s.pendingReverse = s.pendingReverse[1:]
		return Step{
			PayloadID: id,
			Payload:   s.masks[id],
			IsMask:    false,
			Original:  s.originals[id],
		}, true
	}
	if s.nextPair >= s.total {
		s.done = true
		return Step{}, false
	}
	text, cat := s.gen.Next()
	id := pairID(s.nextPair)
	s.nextPair++
	s.originals[id] = text
	return Step{
		PayloadID: id,
		Payload:   text,
		IsMask:    true,
		Original:  text,
		Category:  cat,
	}, true
}

func (s *sequential) Complete(st Step, r Response) {
	if !st.IsMask || !r.IsValidSuccess() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.masks[st.PayloadID]; ok {
		return
	}
	s.masks[st.PayloadID] = r.Result
	s.pendingReverse = append(s.pendingReverse, st.PayloadID)
}

func (s *sequential) Pending() int { return 0 }

// maskDominant issues mostly new masks with a growing set of pending pairs.
type maskDominant struct {
	mu       sync.Mutex
	gen      *TextGenerator
	total    int
	nextPair int
	restore  float64
	rng      *rand.Rand
	// pending maps payload_id to its mask and original.
	pending map[string]pairState
	// pendingOrder keeps insertion order for deterministic restore selection.
	pendingOrder []string
}

type pairState struct {
	mask     string
	original string
}

func newMaskDominant(cfg ScenarioConfig, gen *TextGenerator) *maskDominant {
	return &maskDominant{
		gen:      gen,
		total:    cfg.TotalPairs,
		restore:  cfg.RestoreFraction,
		rng:      rand.New(rand.NewSource(cfg.Seed)),
		pending:  make(map[string]pairState),
	}
}

func (m *maskDominant) Next() (Step, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Prefer a restore when the pending set is non-empty and the random draw
	// falls below the restore fraction.
	if len(m.pendingOrder) > 0 && m.rng.Float64() < m.restore {
		id := m.pendingOrder[m.rng.Intn(len(m.pendingOrder))]
		st := m.pending[id]
		return Step{
			PayloadID: id,
			Payload:   st.mask,
			IsMask:    false,
			Original:  st.original,
		}, true
	}
	if m.nextPair >= m.total {
		// No new pairs left; fall back to restoring pending pairs.
		if len(m.pendingOrder) == 0 {
			return Step{}, false
		}
		id := m.pendingOrder[m.rng.Intn(len(m.pendingOrder))]
		st := m.pending[id]
		return Step{
			PayloadID: id,
			Payload:   st.mask,
			IsMask:    false,
			Original:  st.original,
		}, true
	}
	text, cat := m.gen.Next()
	id := pairID(m.nextPair)
	m.nextPair++
	return Step{
		PayloadID: id,
		Payload:   text,
		IsMask:    true,
		Original:  text,
		Category:  cat,
	}, true
}

func (m *maskDominant) Complete(st Step, r Response) {
	if !st.IsMask || !r.IsValidSuccess() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pending[st.PayloadID]; ok {
		return
	}
	m.pending[st.PayloadID] = pairState{mask: r.Result, original: st.Original}
	m.pendingOrder = append(m.pendingOrder, st.PayloadID)
}

func (m *maskDominant) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pendingOrder)
}

// restoreDominant restores a pre-created set of correspondences. The set is
// built by the preparation phase; each pair has a bounded restore budget so a
// successful restore is not repeated infinitely under one ID.
type restoreDominant struct {
	mu       sync.Mutex
	gen      *TextGenerator
	restore  float64
	rng      *rand.Rand
	// pairs holds the pre-created correspondences.
	pairs []pairState
	// budgets tracks remaining restores per pair index.
	budgets []int
	// nextNew is the index of the next new pair to create (for the small
	// fraction of new masks during the run).
	nextNew int
	total   int
}

func newRestoreDominant(cfg ScenarioConfig, gen *TextGenerator) *restoreDominant {
	return &restoreDominant{
		gen:     gen,
		restore: cfg.RestoreFraction,
		rng:     rand.New(rand.NewSource(cfg.Seed)),
		total:   cfg.TotalPairs,
	}
}

// AddPrepared registers a pre-created correspondence from the preparation phase.
func (r *restoreDominant) AddPrepared(original, mask string, budget int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pairs = append(r.pairs, pairState{mask: mask, original: original})
	r.budgets = append(r.budgets, budget)
}

func (r *restoreDominant) Next() (Step, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Prefer restoring a prepared pair with remaining budget.
	if len(r.pairs) > 0 && r.rng.Float64() < r.restore {
		idx := r.rng.Intn(len(r.pairs))
		if r.budgets[idx] > 0 {
			r.budgets[idx]--
			p := r.pairs[idx]
			return Step{
				PayloadID: prepID(idx),
				Payload:   p.mask,
				IsMask:    false,
				Original:  p.original,
			}, true
		}
	}
	// Otherwise create a new pair (mask step).
	if r.nextNew < r.total {
		text, cat := r.gen.Next()
		id := pairID(r.total + r.nextNew)
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
	for i := range r.pairs {
		if r.budgets[i] > 0 {
			r.budgets[i]--
			p := r.pairs[i]
			return Step{
				PayloadID: prepID(i),
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

// pairID builds a stable payload_id for a pair index.
func pairID(i int) string {
	return "load-" + itoa(i)
}

// prepID builds the payload_id used by the preparation phase for a prepared
// pair. It must match the ID used when the correspondence was created.
func prepID(i int) string {
	return "prep-" + itoa(i)
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