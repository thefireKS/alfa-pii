package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/metrics"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

// newObservableHandler builds a handler with a captured logger and a metrics
// collector so tests can inspect both.
func newObservableHandler(maxActive int, maxBody int64) (*Handler, *bytes.Buffer, *metrics.Metrics) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: 0, CreateWait: time.Second})
	svc := app.New([]app.Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	met := metrics.New()
	return NewHandler(svc, nil, func() bool { return true }, maxActive, maxBody, logger, met), &buf, met
}

// scrapeMetrics returns the raw /metrics body for the handler.
func scrapeMetrics(t *testing.T, h *Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
	return rec.Body.String()
}

// metricValue extracts the value of a metric line by name and label subset.
// Label matching is order-independent: each "key=value" pair must appear in
// the line.
func metricValue(t *testing.T, body, name, labels string) string {
	t.Helper()
	pairs := []string{}
	if labels != "" {
		pairs = strings.Split(labels, ",")
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		matched := true
		for _, p := range pairs {
			if !strings.Contains(line, p) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		// The value is the last whitespace-separated field.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		return fields[len(fields)-1]
	}
	return ""
}

// TestMetricsReflectOperations verifies that successful masks, restorations,
// repeats and refusals are reflected in the metrics.
func TestMetricsReflectOperations(t *testing.T) {
	h, _, met := newObservableHandler(10, 1<<20)
	original := "mail a@b.ru"

	// Mask (new).
	rec := doPost(t, h, `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mask status = %d", rec.Code)
	}
	masked := decodeResult(t, rec)

	// Repeat of original.
	rec = doPost(t, h, `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat status = %d", rec.Code)
	}

	// Restore of mask.
	rec = doPost(t, h, `{"payload":"`+masked+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d", rec.Code)
	}

	// Conflict.
	rec = doPost(t, h, `{"payload":"unrelated","payload_id":"id-1"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", rec.Code)
	}

	body := scrapeMetrics(t, h)
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="mask"`); got != "1" {
		t.Fatalf("mask count = %q, want 1", got)
	}
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="repeat"`); got != "1" {
		t.Fatalf("repeat count = %q, want 1", got)
	}
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="restore"`); got != "1" {
		t.Fatalf("restore count = %q, want 1", got)
	}
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="conflict"`); got != "1" {
		t.Fatalf("conflict count = %q, want 1", got)
	}
	// Text volume was recorded for the successful operations.
	if got := metricValue(t, body, "pii_text_bytes_total", `operation="process"`); got == "" || got == "0" {
		t.Fatalf("text bytes not recorded: %q", got)
	}
	if got := metricValue(t, body, "pii_tokens_total", `operation="process",method="estimate_runes_div4"`); got == "" || got == "0" {
		t.Fatalf("tokens not recorded: %q", got)
	}
	// Duration histogram has a success sample.
	if got := metricValue(t, body, "pii_response_duration_seconds_count", `operation="process",class="success"`); got == "" || got == "0" {
		t.Fatalf("success duration count not recorded: %q", got)
	}
	_ = met
}

// TestMetricsUniquePayloadIDBounded verifies that unique payload_id values do
// not increase the number of label sets: the request counter is keyed only by
// operation and outcome, never by payload_id.
func TestMetricsUniquePayloadIDBounded(t *testing.T) {
	h, _, _ := newObservableHandler(10, 1<<20)
	for i := 0; i < 20; i++ {
		rec := doPost(t, h, `{"payload":"mail a@b.ru","payload_id":"id-`+string(rune('a'+i))+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	body := scrapeMetrics(t, h)
	// Count distinct label sets for pii_requests_total.
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "pii_requests_total{") {
			count++
		}
	}
	// Only operation+outcome combinations appear; payload_id never appears.
	if count == 0 {
		t.Fatalf("no request metric series found")
	}
	if strings.Contains(body, "id-a") || strings.Contains(body, "payload_id") {
		t.Fatalf("payload_id leaked into metrics labels")
	}
}

// TestMetricsOverloadAndRelease verifies that overload returns 429 with
// Retry-After and that releasing the slot returns the service to work.
func TestMetricsOverloadAndRelease(t *testing.T) {
	h, _, _ := newObservableHandler(1, 1<<20)
	// Occupy the single slot.
	h.active <- struct{}{}
	rec := doPost(t, h, `{"payload":"mail a@b.ru","payload_id":"id-1"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("overload status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	<-h.active

	// After release the service works again.
	rec = doPost(t, h, `{"payload":"mail a@b.ru","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("after release status = %d, want 200", rec.Code)
	}
	body := scrapeMetrics(t, h)
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="overload"`); got != "1" {
		t.Fatalf("overload count = %q, want 1", got)
	}
	if got := metricValue(t, body, "pii_response_duration_seconds_count", `operation="process",class="overload"`); got != "1" {
		t.Fatalf("overload duration count = %q, want 1", got)
	}
}

// TestLogsContainStagesNoSensitiveData verifies structured logs contain the
// expected stages and never contain the payload, payload_id or values.
func TestLogsContainStagesNoSensitiveData(t *testing.T) {
	h, buf, _ := newObservableHandler(10, 1<<20)
	secret := "super-secret@example.com"
	rec := doPost(t, h, `{"payload":"mail `+secret+`","payload_id":"id-secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	logs := buf.String()
	for _, stage := range []string{"accept", "operation", "recognition", "replacement", "storage", "complete"} {
		if !strings.Contains(logs, "stage="+stage) {
			t.Fatalf("logs missing stage %q:\n%s", stage, logs)
		}
	}
	// The sensitive value, the payload_id and the email must never appear.
	for _, leak := range []string{secret, "id-secret", "example.com"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("logs leaked %q:\n%s", leak, logs)
		}
	}
	// A technical request identifier is present and distinct from payload_id.
	if !strings.Contains(logs, "request_id=") {
		t.Fatalf("logs missing request_id:\n%s", logs)
	}
}

// TestErrorAndMetricsDoNotLeakSensitive verifies that error responses and the
// metrics output never contain a synthetic sensitive value.
func TestErrorAndMetricsDoNotLeakSensitive(t *testing.T) {
	h, _, _ := newObservableHandler(10, 1<<20)
	secret := "leak-me@example.com"
	// A malformed request that echoes nothing.
	rec := doPost(t, h, `{"payload":"`+secret+`","payload_id":123}`)
	if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
		t.Fatalf("error body leaked payload: %q", rec.Body.String())
	}
	// A successful mask then a conflict; the sensitive value must not appear in
	// metrics.
	doPost(t, h, `{"payload":"mail `+secret+`","payload_id":"id-1"}`)
	doPost(t, h, `{"payload":"other","payload_id":"id-1"}`)
	body := scrapeMetrics(t, h)
	if strings.Contains(body, secret) || strings.Contains(body, "example.com") {
		t.Fatalf("metrics leaked sensitive value")
	}
}

// TestCancellationRecorded verifies a cancelled request is recorded as a
// cancellation outcome.
func TestCancellationRecorded(t *testing.T) {
	h, _, _ := newObservableHandler(10, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/process", strings.NewReader(`{"payload":"mail a@b.ru","payload_id":"id-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	// The operation fails closed; the exact status is not the point here.
	body := scrapeMetrics(t, h)
	if got := metricValue(t, body, "pii_requests_total", `operation="process",outcome="cancel"`); got != "1" {
		t.Fatalf("cancel count = %q, want 1", got)
	}
}

// TestMetricsEndpointRegistered verifies /metrics is served by the routes.
func TestMetricsEndpointRegistered(t *testing.T) {
	h, _, _ := newObservableHandler(10, 1<<20)
	// Make a request so the request counter has a series to expose.
	doPost(t, h, `{"payload":"mail a@b.ru","payload_id":"id-1"}`)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pii_requests_total") {
		t.Fatalf("metrics body missing collectors")
	}
}

// TestLivezReadyzNoUserData verifies health endpoints return fixed status and
// no user data.
func TestLivezReadyzNoUserData(t *testing.T) {
	h, _, _ := newObservableHandler(10, 1<<20)
	for _, path := range []string{"/livez", "/readyz"} {
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		var m map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s body not JSON: %q", path, rec.Body.String())
		}
		if _, ok := m["status"]; !ok {
			t.Fatalf("%s missing status field", path)
		}
	}
}