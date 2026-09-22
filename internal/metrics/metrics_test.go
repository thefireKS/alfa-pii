package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEstimateTokens verifies the token estimator is a documented heuristic
// that never reports zero for non-empty text and is monotonic in length.
func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatalf("empty text should estimate 0 tokens")
	}
	if EstimateTokens("a") != 1 {
		t.Fatalf("single char should estimate 1 token")
	}
	// 4 runes -> 1 token, 5 runes -> 2 tokens.
	if EstimateTokens("abcd") != 1 {
		t.Fatalf("4 runes should estimate 1 token")
	}
	if EstimateTokens("abcde") != 2 {
		t.Fatalf("5 runes should estimate 2 tokens")
	}
	// Cyrillic counts by rune, not byte.
	if EstimateTokens("привет") != 2 {
		t.Fatalf("6 runes should estimate 2 tokens, got %d", EstimateTokens("привет"))
	}
}

// TestMetricsEndpointServesText verifies /metrics returns the Prometheus text
// format and never returns user data.
func TestMetricsEndpointServesText(t *testing.T) {
	m := New()
	m.ObserveRequest(OperationProcess, OutcomeMask, ClassSuccess, 0.01)
	m.ObserveText(OperationProcess, "secret-value@example.com")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"pii_requests_total",
		"pii_response_duration_seconds",
		"pii_text_bytes_total",
		"pii_tokens_total",
		"estimate_runes_div4",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q", want)
		}
	}
	// The sensitive value must never appear in the metrics output.
	if strings.Contains(body, "secret-value@example.com") {
		t.Fatalf("metrics leaked sensitive value")
	}
}

// TestMetricsHandlerBounded verifies the metrics handler completes and does not
// hang when the registry is empty.
func TestMetricsHandlerBounded(t *testing.T) {
	m := New()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, err := io.ReadAll(rec.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
}