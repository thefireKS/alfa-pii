package loadgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var calls int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
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
	resp, attempts := client.SendWithRetry(context.Background(), "id", "text", policy)
	if len(attempts) != 3 {
		t.Errorf("attempts=%d want 3", len(attempts))
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
	resp, attempts := client.SendWithRetry(context.Background(), "id", "text", policy)
	if len(attempts) != 3 {
		t.Errorf("attempts=%d want 3", len(attempts))
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

// TestMaskDominantRestoreCompletesPair verifies that a successfully restored
// pair is removed from the pending set so it is not selected again, while a
// failed restore keeps the pair pending for a retry with the same ID and data.
func TestMaskDominantRestoreCompletesPair(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeMaskDominant, TotalPairs: 10, RestoreFraction: 1.0, Seed: 1, RunID: "run"}, gen)
	// Issue one mask step and complete it so the pair enters pending.
	st, ok := s.Next()
	if !ok || !st.IsMask {
		t.Fatalf("expected mask step, got %+v ok=%v", st, ok)
	}
	s.Complete(st, Response{Status: 200, Outcome: OutcomeOK, Result: "mask" + st.PayloadID})
	if s.Pending() != 1 {
		t.Fatalf("pending=%d want 1", s.Pending())
	}
	// A failed restore (error) keeps the pair pending.
	rst, ok := s.Next()
	if !ok || rst.IsMask {
		t.Fatalf("expected restore step, got %+v ok=%v", rst, ok)
	}
	s.Complete(rst, Response{Status: 500, Outcome: OutcomeError})
	if s.Pending() != 1 {
		t.Errorf("pending=%d want 1 after failed restore", s.Pending())
	}
	// A successful restore removes the pair from pending.
	rst, ok = s.Next()
	if !ok || rst.IsMask {
		t.Fatalf("expected restore step, got %+v ok=%v", rst, ok)
	}
	s.Complete(rst, Response{Status: 200, Outcome: OutcomeOK, Result: rst.Original})
	if s.Pending() != 0 {
		t.Errorf("pending=%d want 0 after successful restore", s.Pending())
	}
	// The pair must not be selected again for restore.
	for i := 0; i < 20; i++ {
		st, ok := s.Next()
		if !ok {
			break
		}
		if !st.IsMask {
			t.Fatalf("restore step issued for a completed pair: %+v", st)
		}
	}
}

func TestRestoreDominantScenarioBudget(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeRestoreDominant, TotalPairs: 0, RestoreFraction: 1.0, RestoreBudget: 2, Seed: 1, RunID: "run"}, gen)
	rd := s.(*restoreDominant)
	rd.AddPrepared("prep-run-0", "original0", "mask0", 2)
	// With budget 2, exactly two restore steps for the pair.
	for i := 0; i < 2; i++ {
		st, ok := s.Next()
		if !ok || st.IsMask {
			t.Fatalf("expected restore step %d, got %+v ok=%v", i, st, ok)
		}
		if st.Payload != "mask0" {
			t.Errorf("payload=%q want mask0", st.Payload)
		}
		if st.PayloadID != "prep-run-0" {
			t.Errorf("payload_id=%q want prep-run-0", st.PayloadID)
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

// TestClientLatencyIncludesBodyWait verifies that the HTTP latency is measured
// until the full response body has been read, not just until the headers
// arrive. The server sends the headers immediately and delays the body; the
// recorded latency must include the body wait. The comparison is not a strict
// benchmark: it only asserts the body wait is not zeroed out.
func TestClientLatencyIncludesBodyWait(t *testing.T) {
	const bodyDelay = 120 * time.Millisecond
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(bodyDelay)
		json.NewEncoder(w).Encode(map[string]string{"result": "masked"})
	})
	client := NewClient(srv.URL, 10, time.Second)
	resp := client.Send(context.Background(), "id", "text")
	if resp.Outcome != OutcomeOK {
		t.Fatalf("outcome=%s want ok", resp.Outcome)
	}
	if resp.Latency < bodyDelay {
		t.Errorf("latency=%s < body delay %s: body wait not included", resp.Latency, bodyDelay)
	}
	if resp.TTFB >= resp.Latency {
		t.Errorf("ttfb=%s >= latency=%s: TTFB should be shorter than full latency", resp.TTFB, resp.Latency)
	}
}

// TestClientReadError verifies that a broken body read is reported as an error
// and not silently accepted.
func TestClientReadError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"result":"partial`))
		// Close the connection without completing the body.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	})
	client := NewClient(srv.URL, 10, time.Second)
	resp := client.Send(context.Background(), "id", "text")
	if resp.Outcome != OutcomeError {
		t.Errorf("outcome=%s want error for broken body", resp.Outcome)
	}
}

// TestClientInvalidResult verifies that a 200 response without a string result
// is classified as invalid, while an empty string result is valid.
func TestClientInvalidResult(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantOK bool
	}{
		{"empty object", `{}`, false},
		{"null result", `{"result":null}`, false},
		{"non-string result", `{"result":123}`, false},
		{"empty string result", `{"result":""}`, true},
		{"valid result", `{"result":"masked"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				w.Write([]byte(c.body))
			})
			client := NewClient(srv.URL, 10, time.Second)
			resp := client.Send(context.Background(), "id", "text")
			if c.wantOK {
				if resp.Outcome != OutcomeOK {
					t.Errorf("outcome=%s want ok for body %q", resp.Outcome, c.body)
				}
			} else if resp.Outcome != OutcomeInvalid {
				t.Errorf("outcome=%s want invalid for body %q", resp.Outcome, c.body)
			}
		})
	}
}

// TestClientReadOverflow verifies that a response body exceeding the read limit
// is detected as an error rather than silently truncated.
func TestClientReadOverflow(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// Write a body larger than maxResponseBytes.
		big := strings.Repeat("x", maxResponseBytes+1024)
		w.Write([]byte(`{"result":"` + big + `"}`))
	})
	client := NewClient(srv.URL, 10, time.Second)
	resp := client.Send(context.Background(), "id", "text")
	if resp.Outcome != OutcomeError {
		t.Errorf("outcome=%s want error for oversized body", resp.Outcome)
	}
}

// singleMaskScenario issues exactly one mask step and then exhausts, so a test
// can isolate the retry series of a single logical operation.
type singleMaskScenario struct {
	issued bool
}

func (s *singleMaskScenario) Next() (Step, bool) {
	if s.issued {
		return Step{}, false
	}
	s.issued = true
	return Step{PayloadID: "id", Payload: "text", IsMask: true, Original: "text"}, true
}

func (s *singleMaskScenario) Complete(Step, Response) {}
func (s *singleMaskScenario) Pending() int            { return 0 }

// TestRunnerRecordsRetrySeries verifies that a 429 -> 503 -> 200 sequence is
// recorded as three sends with three outcomes and one completed logical
// operation.
func TestRunnerRecordsRetrySeries(t *testing.T) {
	var n int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(503)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]string{"result": "masked"})
		}
	})
	client := NewClient(srv.URL, 10, time.Second)
	scenario := &singleMaskScenario{}
	pacer := NewPacer(PacerConfig{Target: 1000, Ramp: 0, Queue: 16})
	defer pacer.Stop()
	runner := NewRunner(client, scenario, pacer, RunnerConfig{
		Workers:               1,
		Retry:                 RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond},
		MaxConsecutiveInvalid: 5,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner.Run(ctx)

	sent, maskSuccess, _, _, _ := runner.Counts()
	if sent != 3 {
		t.Errorf("sent=%d want 3 (three HTTP sends)", sent)
	}
	if maskSuccess != 1 {
		t.Errorf("maskSuccess=%d want 1 (one logical operation)", maskSuccess)
	}
	stats := runner.Stats()
	if got := stats.OutcomeCount(OutcomeOverload); got != 1 {
		t.Errorf("overload count=%d want 1", got)
	}
	if got := stats.OutcomeCount(OutcomeError); got != 1 {
		t.Errorf("error count=%d want 1", got)
	}
	if got := stats.OutcomeCount(OutcomeMask); got != 1 {
		t.Errorf("mask count=%d want 1", got)
	}
	// The logical operation duration must be recorded.
	if op := stats.OpLatency(); op.Mean <= 0 {
		t.Errorf("op latency mean=%s want > 0", op.Mean)
	}
}

// TestSchedulerRecordsRetrySeries verifies the scheduler records each actual
// HTTP send of a retry series and one completed logical operation.
func TestSchedulerRecordsRetrySeries(t *testing.T) {
	var n int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(503)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]string{"result": "masked"})
		}
	})
	client := NewClient(srv.URL, 10, time.Second)
	scenario := &singleMaskScenario{}
	sched := NewScheduler(client, scenario, SchedulerConfig{
		Target:  1000,
		Workers: 1,
		Queue:   16,
		Retry:   RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sched.Run(ctx)

	_, sent, _, _, maskSuccess, _, _, _ := sched.Counts()
	if sent != 3 {
		t.Errorf("sent=%d want 3 (three HTTP sends)", sent)
	}
	if maskSuccess != 1 {
		t.Errorf("maskSuccess=%d want 1 (one logical operation)", maskSuccess)
	}
	stats := sched.Stats()
	if got := stats.OutcomeCount(OutcomeOverload); got != 1 {
		t.Errorf("overload count=%d want 1", got)
	}
	if got := stats.OutcomeCount(OutcomeError); got != 1 {
		t.Errorf("error count=%d want 1", got)
	}
}

// TestSchedulerCompatibilityCheck verifies that five consecutive invalid
// responses stop the whole scheduler run, not just one worker.
func TestSchedulerCompatibilityCheck(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	client := NewClient(srv.URL, 10, time.Second)
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	scenario := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 1000, Seed: 1, RunID: "run"}, gen)
	sched := NewScheduler(client, scenario, SchedulerConfig{
		Target:                1000,
		Workers:               4,
		Queue:                 16,
		Retry:                 RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond},
		MaxConsecutiveInvalid: 5,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason := sched.Run(ctx)
	if reason != "five consecutive invalid responses" {
		t.Errorf("stop reason=%q want compatibility stop", reason)
	}
}

// TestSchedulerExhaustsWithoutTimeout verifies that when the scenario is
// exhausted the scheduler finishes promptly instead of waiting for the overall
// timeout.
func TestSchedulerExhaustsWithoutTimeout(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"result": "masked"})
	})
	client := NewClient(srv.URL, 10, time.Second)
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	scenario := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 3, Seed: 1, RunID: "run"}, gen)
	sched := NewScheduler(client, scenario, SchedulerConfig{
		Target:                1000,
		Workers:               2,
		Queue:                 4,
		Retry:                 RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond},
		MaxConsecutiveInvalid: 5,
	})
	// A long timeout that would mask a hang; the run must finish well before it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	reason := sched.Run(ctx)
	if reason != "scenario exhausted" {
		t.Errorf("stop reason=%q want scenario exhausted", reason)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("scheduler took %s to exhaust, expected prompt finish", elapsed)
	}
}

// TestRestoreDominantPrepSkipKeepsIDs verifies that when a preparation
// correspondence is skipped after an error, the remaining pairs still restore
// under their exact stored payload_id rather than a guessed index-based ID.
func TestRestoreDominantPrepSkipKeepsIDs(t *testing.T) {
	gen := NewTextGenerator(1, DefaultSizeProfile(), 120, 400, 2000)
	s := NewScenario(ScenarioConfig{Mode: ModeRestoreDominant, TotalPairs: 0, RestoreFraction: 1.0, RestoreBudget: 1, Seed: 1, RunID: "run"}, gen)
	rd := s.(*restoreDominant)
	// Simulate preparation where index 0 failed and was skipped: only indices
	// 1 and 2 were created, with their real payload_ids.
	rd.AddPrepared("prep-run-1", "original1", "mask1", 1)
	rd.AddPrepared("prep-run-2", "original2", "mask2", 1)
	// The restore must use one of the stored IDs (never the skipped prep-run-0)
	// and the mask stored under that exact ID.
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		st, ok := s.Next()
		if !ok || st.IsMask {
			t.Fatalf("expected restore step %d, got %+v ok=%v", i, st, ok)
		}
		if st.PayloadID == "prep-run-0" {
			t.Errorf("restore used skipped index ID prep-run-0")
		}
		if st.PayloadID == "prep-run-1" && st.Payload != "mask1" {
			t.Errorf("payload=%q want mask1 for prep-run-1", st.Payload)
		}
		if st.PayloadID == "prep-run-2" && st.Payload != "mask2" {
			t.Errorf("payload=%q want mask2 for prep-run-2", st.Payload)
		}
		seen[st.PayloadID] = true
	}
	if !seen["prep-run-1"] || !seen["prep-run-2"] {
		t.Errorf("expected both stored IDs to be restored, got %v", seen)
	}
}

// TestRunIDEmbeddedInPayloadID verifies that the run_id is part of every
// payload_id and that different run_ids produce different IDs for the same pair
// index while the generated text stays the same.
func TestRunIDEmbeddedInPayloadID(t *testing.T) {
	gen1 := NewTextGenerator(7, DefaultSizeProfile(), 120, 400, 2000)
	gen2 := NewTextGenerator(7, DefaultSizeProfile(), 120, 400, 2000)
	s1 := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 2, Seed: 7, RunID: "runA"}, gen1)
	s2 := NewScenario(ScenarioConfig{Mode: ModeSequential, TotalPairs: 2, Seed: 7, RunID: "runB"}, gen2)
	st1, _ := s1.Next()
	st2, _ := s2.Next()
	if st1.PayloadID == st2.PayloadID {
		t.Errorf("payload_ids should differ across runs: %q", st1.PayloadID)
	}
	if !strings.Contains(st1.PayloadID, "runA") {
		t.Errorf("payload_id %q does not contain run_id runA", st1.PayloadID)
	}
	if st1.Payload != st2.Payload {
		t.Errorf("texts should be reproducible across runs: %q vs %q", st1.Payload, st2.Payload)
	}
}
