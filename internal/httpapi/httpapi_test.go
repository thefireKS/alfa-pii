package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

func newTestHandler(maxActive int, maxBody int64) *Handler {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, TTL: 0})
	svc := app.New([]app.Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	return NewHandler(svc, func() bool { return true }, maxActive, maxBody)
}

func doPost(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/process", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp processResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%q", err, rec.Body.String())
	}
	return resp.Result
}

func TestProcessFullCycle(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	original := "contact a.b@example.com now"

	// First call masks.
	rec := doPost(t, h, `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	masked := decodeResult(t, rec)
	if masked == original || strings.Contains(masked, "a.b@example.com") {
		t.Fatalf("masked = %q", masked)
	}

	// Repeat of original returns same mask.
	rec2 := doPost(t, h, `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("repeat status = %d", rec2.Code)
	}
	if got := decodeResult(t, rec2); got != masked {
		t.Fatalf("repeat = %q, want %q", got, masked)
	}

	// Repeat of mask returns exact original.
	rec3 := doPost(t, h, `{"payload":"`+masked+`","payload_id":"id-1"}`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body=%q", rec3.Code, rec3.Body.String())
	}
	if got := decodeResult(t, rec3); got != original {
		t.Fatalf("restore = %q, want %q", got, original)
	}
}

func TestProcessNoPIIEmptyCyrillicEmoji(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	cases := []struct {
		name string
		text string
	}{
		{name: "no pii", text: "просто текст"},
		{name: "empty", text: ""},
		{name: "cyrillic", text: "привет мир"},
		{name: "emoji", text: "привет 👋 мир"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, _ := json.Marshal(processRequest{Payload: c.text, PayloadID: "id-" + c.name})
			rec := doPost(t, h, string(body))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
			}
			if got := decodeResult(t, rec); got != c.text {
				t.Fatalf("got %q, want %q", got, c.text)
			}
		})
	}
}

func TestProcessMultipleEmails(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	original := "a@b.ru and c@d.io"
	rec := doPost(t, h, `{"payload":"`+original+`","payload_id":"id-multi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	masked := decodeResult(t, rec)
	if strings.Contains(masked, "a@b.ru") || strings.Contains(masked, "c@d.io") {
		t.Fatalf("masked still contains emails: %q", masked)
	}
	rec2 := doPost(t, h, `{"payload":"`+masked+`","payload_id":"id-multi"}`)
	if got := decodeResult(t, rec2); got != original {
		t.Fatalf("restore = %q, want %q", got, original)
	}
}

func TestProcessConflict(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	doPost(t, h, `{"payload":"a@b.ru","payload_id":"id-conf"}`)
	rec := doPost(t, h, `{"payload":"unrelated","payload_id":"id-conf"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

func TestProcessInvalidFields(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "empty payload_id", body: `{"payload":"x","payload_id":""}`, want: http.StatusBadRequest},
		{name: "missing payload_id", body: `{"payload":"x"}`, want: http.StatusBadRequest},
		{name: "wrong payload type", body: `{"payload":123,"payload_id":"id"}`, want: http.StatusBadRequest},
		{name: "wrong payload_id type", body: `{"payload":"x","payload_id":5}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"payload":"x","payload_id":"id","extra":1}`, want: http.StatusBadRequest},
		{name: "not json", body: `not json`, want: http.StatusBadRequest},
		{name: "array not object", body: `[1,2]`, want: http.StatusBadRequest},
		{name: "trailing data", body: `{"payload":"x","payload_id":"id"} {}`, want: http.StatusBadRequest},
		{name: "wrong method", body: ``, want: http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if c.name == "wrong method" {
				req := httptest.NewRequest(http.MethodGet, "/process", nil)
				rec = httptest.NewRecorder()
				h.Routes().ServeHTTP(rec, req)
			} else {
				rec = doPost(t, h, c.body)
			}
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, c.want, rec.Body.String())
			}
			// Error body must be valid JSON and must not echo the payload.
			var e errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("error body not JSON: %q", rec.Body.String())
			}
			if e.Error == "" {
				t.Fatalf("error message empty: %q", rec.Body.String())
			}
		})
	}
}

func TestProcessBodyTooLarge(t *testing.T) {
	h := newTestHandler(10, 16)
	big := strings.Repeat("a", 64)
	rec := doPost(t, h, `{"payload":"`+big+`","payload_id":"id-big"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%q", rec.Code, rec.Body.String())
	}
}

func TestProcessActiveLimit(t *testing.T) {
	h := newTestHandler(1, 1<<20)
	// First request occupies the single slot.
	req := httptest.NewRequest(http.MethodPost, "/process", strings.NewReader(`{"payload":"x","payload_id":"id"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.active <- struct{}{} // occupy the slot
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	<-h.active
}

func TestLivezReadyz(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("livez status = %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", rec2.Code)
	}
}

func TestReadyzNotReady(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 10, MaxBytes: 1 << 20, TTL: 0})
	svc := app.New([]app.Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	h := NewHandler(svc, func() bool { return false }, 10, 1<<20)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
}

func TestErrorBodyDoesNotLeakPayload(t *testing.T) {
	h := newTestHandler(10, 1<<20)
	secret := "super-secret-value"
	rec := doPost(t, h, `{"payload":"`+secret+`","payload_id":123}`)
	if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
		t.Fatalf("error body leaked payload: %q", rec.Body.String())
	}
}
