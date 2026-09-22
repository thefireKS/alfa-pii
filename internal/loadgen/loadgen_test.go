package loadgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestServer returns an httptest server that responds to /process according
// to the given handler.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientClassifiesResponses(t *testing.T) {
	// Classification is a pure function of the Response; verify the valid
	// success predicate and the outcome set.
	cases := []struct {
		name string
		resp Response
		want bool
	}{
		{"ok", Response{Status: 200, Outcome: OutcomeOK}, true},
		{"invalid", Response{Status: 200, Outcome: OutcomeInvalid}, false},
		{"error", Response{Status: 500, Outcome: OutcomeError}, false},
		{"timeout", Response{Status: 0, Outcome: OutcomeTimeout}, false},
		{"overload", Response{Status: 429, Outcome: OutcomeOverload}, false},
	}
	for _, c := range cases {
		if got := c.resp.IsValidSuccess(); got != c.want {
			t.Errorf("%s: IsValidSuccess()=%v want %v", c.name, got, c.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"2", 2 * time.Second},
		{"", 0},
		{"abc", 0},
		{"-1", 0},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in); got != c.want {
			t.Errorf("parseRetryAfter(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestSendWithRetryRetriesOn429(t *testing.T) {
	var attempts int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"result": "masked"})
	})
	client := NewClient(srv.URL, 10, time.Second)
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}
	resp, n := client.SendWithRetry(context.Background(), "id", "text", policy)
	if n != 3 {
		t.Errorf("attempts=%d want 3", n)
	}
	if resp.Outcome != OutcomeOK {
		t.Errorf("outcome=%s want ok", resp.Outcome)
	}
}

func TestSendWithRetryStopsAfterMax(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	client := NewClient(srv.URL, 10, time.Second)
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}
	resp, n := client.SendWithRetry(context.Background(), "id", "text", policy)
	if n != 3 {
		t.Errorf("attempts=%d want 3", n)
	}
	if resp.Outcome != OutcomeError {
		t.Errorf("outcome=%s want error", resp.Outcome)
	}
}

func TestTextGeneratorDeterministic(t *testing.T) {
	g1 := NewTextGenerator(42, DefaultSizeProfile(), 120, 400, 2000)
	g2 := NewTextGenerator(42, DefaultSizeProfile(), 120, 400, 2000)
	for i := 0; i < 100; i++ {
		t1, _ := g1.Next()
		t2, _ := g2.Next()
		if t1 != t2 {
			t.Fatalf("generator not deterministic at %d", i)
		}
	}
}

func TestSequentialScenario(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 2, Seed: 1}, gen)
	// Pair 0 forward.
	st, ok := s.Next()
	if !ok || !st.IsMask {
		t.Fatalf("expected mask step, got %+v ok=%v", st, ok)
	}
	s.Complete(st, Response{Status: 200, Outcome: OutcomeOK, Result: "mask0"})
	// Pair 0 reverse is now available.
	st, ok = s.Next()
	if !ok || st.IsMask {
		t.Fatalf("expected restore step, got %+v ok=%v", st, ok)
	}
	if st.Payload != "mask0" {
		t.Errorf("restore payload=%q want mask0", st.Payload)
	}
	// A restore must never be issued before its forward completes: with no
	// completed forward, Next returns a new forward step.
	st, ok = s.Next()
	if !ok || !st.IsMask {
		t.Fatalf("expected mask step before any completed forward, got %+v ok=%v", st, ok)
	}
}

func TestMaskDominantScenario(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeMaskDominant, TotalPairs: 10, RestoreFraction: 0.5, Seed: 1}, gen)
	// Issue mask steps and complete them to grow the pending set. With a
	// restore fraction of 0.5, some steps may be restores; complete only the
	// mask steps and stop once the pending set reaches 5.
	masks := 0
	for masks < 5 {
		st, ok := s.Next()
		if !ok {
			t.Fatalf("scenario exhausted before 5 masks, got %d", masks)
		}
		if st.IsMask {
			s.Complete(st, Response{Status: 200, Outcome: OutcomeOK, Result: "mask" + st.PayloadID})
			masks++
		}
	}
	if s.Pending() != 5 {
		t.Errorf("pending=%d want 5", s.Pending())
	}
	// With restore fraction 0.5 and a non-empty pending set, some steps should
	// be restores.
	sawRestore := false
	for i := 0; i < 50; i++ {
		st, ok := s.Next()
		if !ok {
			break
		}
		if !st.IsMask {
			sawRestore = true
			break
		}
	}
	if !sawRestore {
		t.Error("expected at least one restore step")
	}
}

func TestRestoreDominantScenarioBudget(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeRestoreDominant, TotalPairs: 0, RestoreFraction: 1.0, RestoreBudget: 2, Seed: 1}, gen)
	rd := s.(*restoreDominant)
	rd.AddPrepared("original0", "mask0", 2)
	// With budget 2, exactly two restore steps for the pair.
	for i := 0; i < 2; i++ {
		st, ok := s.Next()
		if !ok || st.IsMask {
			t.Fatalf("expected restore step %d, got %+v ok=%v", i, st, ok)
		}
		if st.Payload != "mask0" {
			t.Errorf("payload=%q want mask0", st.Payload)
		}
	}
	// Budget exhausted; with no new pairs and no budget left, scenario ends.
	if _, ok := s.Next(); ok {
		t.Error("expected scenario exhausted after budget")
	}
}

func TestPacerCounts(t *testing.T) {
	p := NewPacer(PacerConfig{Target: 100, Ramp: 0, Queue: 4})
	defer p.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	consumed := 0
	for {
		if !p.Wait(ctx) {
			break
		}
		consumed++
	}
	if consumed == 0 {
		t.Error("expected at least one consumed slot")
	}
	granted, c, skipped := p.Counts()
	if c != int64(consumed) {
		t.Errorf("consumed=%d want %d", c, consumed)
	}
	if granted < c {
		t.Errorf("granted=%d < consumed=%d", granted, c)
	}
	_ = skipped
}

func TestRunnerCompatibilityCheck(t *testing.T) {
	// A server that returns 500 for every request: the runner should stop
	// after five consecutive invalid responses.
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	client := NewClient(srv.URL, 10, time.Second)
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	scenario := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 1000, Seed: 1}, gen)
	pacer := NewPacer(PacerConfig{Target: 1000, Ramp: 0, Queue: 16})
	defer pacer.Stop()
	runner := NewRunner(client, scenario, pacer, RunnerConfig{
		Workers:               4,
		Retry:                 RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond},
		MaxConsecutiveInvalid: 5,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason := runner.Run(ctx)
	if reason != "five consecutive invalid responses" {
		t.Errorf("stop reason=%q want compatibility stop", reason)
	}
}

func TestParseMetricsScientificNotation(t *testing.T) {
	body := `pii_store_records 5210
pii_store_bytes 4.08003e+06
pii_store_ttl_expired_total 3
pii_store_failures_total{reason="capacity"} 2
pii_active_requests 7
pii_requests_total{operation="process",outcome="mask"} 100`
	var sm ServiceMetrics
	sm.RequestsTotal = make(map[string]int64)
	sm.StoreFailures = make(map[string]int64)
	parseMetrics(body, &sm)
	if sm.StoreRecords != 5210 {
		t.Errorf("records=%d want 5210", sm.StoreRecords)
	}
	if sm.StoreBytes != 4080030 {
		t.Errorf("bytes=%d want 4080030", sm.StoreBytes)
	}
	if sm.StoreTTL != 3 {
		t.Errorf("ttl=%d want 3", sm.StoreTTL)
	}
	if sm.StoreFailures["capacity"] != 2 {
		t.Errorf("failures=%d want 2", sm.StoreFailures["capacity"])
	}
	if sm.Active != 7 {
		t.Errorf("active=%d want 7", sm.Active)
	}
	if sm.RequestsTotal["process|mask"] != 100 {
		t.Errorf("requests=%d want 100", sm.RequestsTotal["process|mask"])
	}
}

func TestParseMem(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"123.4MiB", 129394278},
		{"1.5GiB", 1610612736},
		{"512KiB", 524288},
		{"100B", 100},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseMem(c.in); got != c.want {
			t.Errorf("parseMem(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestRunner429DoesNotResetCounter(t *testing.T) {
	// A server that returns 429 then 500 repeatedly. The 429 must not reset the
	// consecutive-invalid counter, so after 429,500,500,500,500 the runner
	// stops.
	var n int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n%2 == 1 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(500)
	})
	client := NewClient(srv.URL, 10, time.Second)
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	scenario := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 1000, Seed: 1}, gen)
	pacer := NewPacer(PacerConfig{Target: 1000, Ramp: 0, Queue: 16})
	defer pacer.Stop()
	runner := NewRunner(client, scenario, pacer, RunnerConfig{
		Workers:               1,
		Retry:                 RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond},
		MaxConsecutiveInvalid: 5,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason := runner.Run(ctx)
	if reason != "five consecutive invalid responses" {
		t.Errorf("stop reason=%q want compatibility stop", reason)
	}
}