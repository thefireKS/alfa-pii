package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

func newConsumerHandler(consumers []app.Consumer, secrets map[string]string) (*Handler, error) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	processTypes := []recognizer.Type{recognizer.Email}
	svc, err := app.NewManaged(reg, processTypes, consumers, st, st, masker.New("PII"))
	if err != nil {
		return nil, err
	}
	return NewHandler(svc, NewAuthenticator(secrets), func() bool { return true }, 10, 1<<20, nil, nil), nil
}

func emailConsumer(name string) app.Consumer {
	return app.Consumer{
		Name:           name,
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.Email},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     app.FormatMarker,
	}
}

func doAuthed(t *testing.T, h *Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

func TestMaskRestoreAuthAndCycle(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha")},
		map[string]string{"alpha-secret": "alpha"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	original := "mail a@b.ru"

	// Missing token -> 401.
	rec := doAuthed(t, h, "/v1/mask", "", `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", rec.Code)
	}
	// Invalid token -> 401.
	rec = doAuthed(t, h, "/v1/mask", "wrong", `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", rec.Code)
	}
	// Valid token masks.
	rec = doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mask status = %d, body=%q", rec.Code, rec.Body.String())
	}
	masked := decodeResult(t, rec)
	if masked == original || strings.Contains(masked, "a@b.ru") {
		t.Fatalf("masked = %q", masked)
	}
	// Restore with the same token returns the original.
	rec = doAuthed(t, h, "/v1/restore", "alpha-secret", `{"payload":"`+masked+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body=%q", rec.Code, rec.Body.String())
	}
	if got := decodeResult(t, rec); got != original {
		t.Fatalf("restore = %q, want %q", got, original)
	}
}

func TestMaskNeverDemasksOverHTTP(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha")},
		map[string]string{"alpha-secret": "alpha"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	original := "mail a@b.ru"
	rec := doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+original+`","payload_id":"id-1"}`)
	masked := decodeResult(t, rec)
	// Passing the mask to /v1/mask returns the mask, not the original.
	rec2 := doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+masked+`","payload_id":"id-1"}`)
	if got := decodeResult(t, rec2); got != masked {
		t.Fatalf("mask of mask = %q, want %q", got, masked)
	}
}

func TestConsumerIsolationOverHTTP(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha"), emailConsumer("beta")},
		map[string]string{"alpha-secret": "alpha", "beta-secret": "beta"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	originalA := "mail a@b.ru"
	originalB := "mail c@d.io"
	ra := doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+originalA+`","payload_id":"id-1"}`)
	rb := doAuthed(t, h, "/v1/mask", "beta-secret", `{"payload":"`+originalB+`","payload_id":"id-1"}`)
	ma, mb := decodeResult(t, ra), decodeResult(t, rb)
	// Beta restoring alpha's mask must not recover alpha's original.
	rec := doAuthed(t, h, "/v1/restore", "beta-secret", `{"payload":"`+ma+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("beta restore alpha mask status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := decodeResult(t, rec); got == originalA {
		t.Fatalf("beta recovered alpha original: %q", got)
	}
	// Beta restores its own mask.
	rec = doAuthed(t, h, "/v1/restore", "beta-secret", `{"payload":"`+mb+`","payload_id":"id-1"}`)
	if got := decodeResult(t, rec); got != originalB {
		t.Fatalf("beta restore = %q, want %q", got, originalB)
	}
}

func TestIdentitySpoofingDoesNotOpenOtherScope(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha"), emailConsumer("beta")},
		map[string]string{"alpha-secret": "alpha", "beta-secret": "beta"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	original := "mail a@b.ru"
	// Alpha masks with its own secret.
	ra := doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+original+`","payload_id":"id-1"}`)
	ma := decodeResult(t, ra)

	// A client-controlled header claiming beta must not change alpha's
	// identity: identity comes from the Bearer secret only.
	req := httptest.NewRequest(http.MethodPost, "/v1/restore", strings.NewReader(`{"payload":"`+ma+`","payload_id":"id-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer alpha-secret")
	req.Header.Set("X-Consumer", "beta")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spoofed restore status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := decodeResult(t, rec); got != original {
		t.Fatalf("spoofed restore = %q, want %q", got, original)
	}

	// /process ignores arbitrary system headers and stays in its own scope.
	req2 := httptest.NewRequest(http.MethodPost, "/process", strings.NewReader(`{"payload":"`+original+`","payload_id":"id-1"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Consumer", "beta")
	rec2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("process with header status = %d, want 200", rec2.Code)
	}
	// The /process scope is separate: it has no correspondence for id-1, so it
	// masks fresh and does not return alpha's stored original.
	if got := decodeResult(t, rec2); got == original {
		t.Fatalf("process leaked consumer original: %q", got)
	}
}

func TestDisabledAndPolicyOverHTTP(t *testing.T) {
	disabled := emailConsumer("off")
	disabled.Enabled = false
	noMask := emailConsumer("nomask")
	noMask.MaskingEnabled = false
	noRestore := emailConsumer("norestore")
	noRestore.CanRestore = false
	h, err := newConsumerHandler(
		[]app.Consumer{disabled, noMask, noRestore},
		map[string]string{"off-secret": "off", "nomask-secret": "nomask", "norestore-secret": "norestore"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	// Disabled system -> 403.
	rec := doAuthed(t, h, "/v1/mask", "off-secret", `{"payload":"a@b.ru","payload_id":"id"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled status = %d, want 403", rec.Code)
	}
	// Masking disabled -> 403.
	rec = doAuthed(t, h, "/v1/mask", "nomask-secret", `{"payload":"a@b.ru","payload_id":"id"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-mask status = %d, want 403", rec.Code)
	}
	// Restore forbidden -> 403.
	rec = doAuthed(t, h, "/v1/restore", "norestore-secret", `{"payload":"x","payload_id":"id"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-restore status = %d, want 403", rec.Code)
	}
}

func TestRestoreNotFoundOverHTTP(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha")},
		map[string]string{"alpha-secret": "alpha"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	rec := doAuthed(t, h, "/v1/restore", "alpha-secret", `{"payload":"x","payload_id":"missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%q", rec.Code, rec.Body.String())
	}
}

func TestV1InvalidBody(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha")},
		map[string]string{"alpha-secret": "alpha"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "empty payload_id", body: `{"payload":"x","payload_id":""}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"payload":"x","payload_id":"id","extra":1}`, want: http.StatusBadRequest},
		{name: "wrong method", body: ``, want: http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder
			if c.name == "wrong method" {
				req := httptest.NewRequest(http.MethodGet, "/v1/mask", nil)
				req.Header.Set("Authorization", "Bearer alpha-secret")
				rec = httptest.NewRecorder()
				h.Routes().ServeHTTP(rec, req)
			} else {
				rec = doAuthed(t, h, "/v1/mask", "alpha-secret", c.body)
			}
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, c.want, rec.Body.String())
			}
			var e errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("error body not JSON: %q", rec.Body.String())
			}
		})
	}
}

// TestMaskRestoreDistinctMarkersOverHTTP verifies the full /v1/mask ->
// /v1/restore cycle for a separate synthetic consumer when the input already
// contains a literal marker that occupies a low number. Distinct original
// values must receive distinct markers so restoration is exact.
func TestMaskRestoreDistinctMarkersOverHTTP(t *testing.T) {
	h, err := newConsumerHandler(
		[]app.Consumer{emailConsumer("alpha")},
		map[string]string{"alpha-secret": "alpha"},
	)
	if err != nil {
		t.Fatalf("newConsumerHandler: %v", err)
	}
	original := "[PII_0] email alice@example.test, второй bob@example.test"
	rec := doAuthed(t, h, "/v1/mask", "alpha-secret", `{"payload":"`+original+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mask status = %d, body=%q", rec.Code, rec.Body.String())
	}
	masked := decodeResult(t, rec)
	if strings.Contains(masked, "alice@example.test") || strings.Contains(masked, "bob@example.test") {
		t.Fatalf("masked %q still contains original emails", masked)
	}
	// Restore the exact mask and verify the original is recovered precisely.
	rec = doAuthed(t, h, "/v1/restore", "alpha-secret", `{"payload":"`+masked+`","payload_id":"id-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body=%q", rec.Code, rec.Body.String())
	}
	if got := decodeResult(t, rec); got != original {
		t.Fatalf("restore = %q, want %q (masked=%q)", got, original, masked)
	}
}
