package demo

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/httpapi"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

// testServer builds a real service over the /v1/mask and /v1/restore endpoints
// with the given consumers and secret map, and returns the base URL.
func testServer(t *testing.T, consumers []app.Consumer, secrets map[string]string) string {
	t.Helper()
	reg := recognizer.NewRegistry()
	processTypes := []recognizer.Type{recognizer.Email, recognizer.Phone, recognizer.FullName, recognizer.Passport}
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	svc, err := app.NewManaged(reg, processTypes, consumers, st, masker.New("PII"))
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(svc, httpapi.NewAuthenticator(secrets), func() bool { return true }, 10, 1<<20).Routes())
	t.Cleanup(srv.Close)
	return srv.URL
}

func demoConsumer(name string) app.Consumer {
	return app.Consumer{
		Name:           name,
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.FullName, recognizer.Phone, recognizer.Email, recognizer.Passport},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     app.FormatMarker,
	}
}

// recordingModel captures the prompt it receives so tests can assert what the
// model saw.
type recordingModel struct {
	prompt string
	calls  int
}

func (m *recordingModel) Complete(_ context.Context, prompt string) (string, error) {
	m.calls++
	m.prompt = prompt
	return DemoModel{}.Complete(context.Background(), prompt)
}

// failingModel returns an error for every call.
type failingModel struct{}

func (failingModel) Complete(context.Context, string) (string, error) {
	return "", errors.New("model failure")
}

const demoOriginal = "ФИО: Иванов Иван Иванович, телефон +7 900 123-45-67, email ivan@example.ru, паспорт 4500 123456."

// TestModelReceivesMaskNotOriginal verifies that the model receives exactly the
// mask and never the original sensitive values.
func TestModelReceivesMaskNotOriginal(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")
	model := &recordingModel{}
	runner := NewRunner(client, client, model)

	res, err := runner.Run(context.Background(), "id-1", demoOriginal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
	if model.prompt != res.Masked {
		t.Fatalf("model prompt = %q, want mask %q", model.prompt, res.Masked)
	}
	for _, secret := range []string{"Иванов", "900 123-45-67", "ivan@example.ru", "4500 123456"} {
		if strings.Contains(model.prompt, secret) {
			t.Fatalf("model received original value %q in prompt %q", secret, model.prompt)
		}
	}
	if !strings.Contains(model.prompt, "[PII_") {
		t.Fatalf("model prompt %q has no markers", model.prompt)
	}
}

// TestRestoreInsideModifiedResponse verifies that restoration substitutes only
// the markers present in the model answer, with a repeated marker and a dropped
// marker, and never returns the stored original wholesale.
func TestRestoreInsideModifiedResponse(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")
	runner := NewRunner(client, client, DemoModel{})

	res, err := runner.Run(context.Background(), "id-1", demoOriginal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The model answer reorders (PII_3 before PII_0), repeats PII_0 and drops
	// PII_1 and PII_2. Restoration must substitute only present markers.
	if !strings.Contains(res.Restored, "4500 123456") {
		t.Fatalf("restored %q missing reordered passport value", res.Restored)
	}
	if got := strings.Count(res.Restored, "Иванов Иван Иванович"); got != 2 {
		t.Fatalf("restored %q repeats full name %d times, want 2", res.Restored, got)
	}
	// Dropped markers' values must not appear.
	if strings.Contains(res.Restored, "900 123-45-67") || strings.Contains(res.Restored, "ivan@example.ru") {
		t.Fatalf("restored %q contains dropped values", res.Restored)
	}
	// The answer is signed as a demonstration.
	if !strings.HasPrefix(res.Restored, "[demo]") {
		t.Fatalf("restored %q not signed as demo", res.Restored)
	}
	// The stored original is not returned wholesale.
	if res.Restored == demoOriginal {
		t.Fatalf("restored equals stored original wholesale")
	}
}

// TestForeignMarkerAndOtherIDNotRevealed verifies that a marker from another ID
// or a damaged marker cannot open data of another correspondence.
func TestForeignMarkerAndOtherIDNotRevealed(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")

	// Mask id-1 and id-2 with different originals.
	m1, err := client.Mask(context.Background(), "id-1", demoOriginal)
	if err != nil {
		t.Fatalf("mask id-1: %v", err)
	}
	other := "ФИО: Петров Пётр Петрович, телефон +7 911 222-33-44, email petr@example.org, паспорт 1234 567890."
	m2, err := client.Mask(context.Background(), "id-2", other)
	if err != nil {
		t.Fatalf("mask id-2: %v", err)
	}

	// Restoring id-1's mask under id-2 must not reveal id-1's values: the
	// markers are interpreted against id-2's table only.
	got, err := client.Restore(context.Background(), "id-2", m1)
	if err != nil {
		t.Fatalf("restore foreign mask: %v", err)
	}
	if strings.Contains(got, "Иванов") || strings.Contains(got, "ivan@example.ru") {
		t.Fatalf("foreign mask revealed id-1 values: %q", got)
	}
	// Restoring id-2's own mask returns id-2's values.
	got2, err := client.Restore(context.Background(), "id-2", m2)
	if err != nil {
		t.Fatalf("restore own mask: %v", err)
	}
	if !strings.Contains(got2, "Петров Пётр Петрович") {
		t.Fatalf("own restore = %q, want id-2 values", got2)
	}

	// A damaged marker (not in the table) is preserved, not resolved.
	damaged := strings.Replace(m1, "[PII_0]", "[PII_99]", 1)
	got3, err := client.Restore(context.Background(), "id-1", damaged)
	if err != nil {
		t.Fatalf("restore damaged mask: %v", err)
	}
	if !strings.Contains(got3, "[PII_99]") {
		t.Fatalf("damaged marker not preserved: %q", got3)
	}
	if strings.Contains(got3, "Иванов") {
		t.Fatalf("damaged marker revealed value: %q", got3)
	}
}

// TestOtherConsumerNotRevealed verifies that a consumer cannot restore another
// consumer's markers.
func TestOtherConsumerNotRevealed(t *testing.T) {
	base := testServer(t,
		[]app.Consumer{demoConsumer("alpha"), demoConsumer("beta")},
		map[string]string{"alpha-secret": "alpha", "beta-secret": "beta"},
	)
	alpha := NewClient(base, "alpha-secret")
	beta := NewClient(base, "beta-secret")

	ma, err := alpha.Mask(context.Background(), "id-1", demoOriginal)
	if err != nil {
		t.Fatalf("alpha mask: %v", err)
	}
	// Beta restoring alpha's mask must not reveal alpha's values. Beta has no
	// correspondence for id-1 in its own scope, so the restore either fails
	// (404) or succeeds with alpha's markers preserved; either way alpha's
	// values must not appear.
	got, err := beta.Restore(context.Background(), "id-1", ma)
	if err == nil && (strings.Contains(got, "Иванов") || strings.Contains(got, "ivan@example.ru")) {
		t.Fatalf("beta revealed alpha values: %q", got)
	}
}

// TestRestoreRightDenied verifies that a consumer without the restore right
// cannot restore values.
func TestRestoreRightDenied(t *testing.T) {
	c := demoConsumer("norestore")
	c.CanRestore = false
	base := testServer(t, []app.Consumer{c}, map[string]string{"norestore-secret": "norestore"})
	client := NewClient(base, "norestore-secret")

	masked, err := client.Mask(context.Background(), "id-1", "mail a@b.ru")
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if _, err := client.Restore(context.Background(), "id-1", masked); err == nil {
		t.Fatal("restore without right succeeded, want error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Fatalf("restore without right err = %v, want 403", err)
	}
}

// TestMaskingErrorNoModelCall verifies that when masking fails, the model is
// never called.
func TestMaskingErrorNoModelCall(t *testing.T) {
	failMasker := &failingMasker{}
	model := &recordingModel{}
	runner := NewRunner(failMasker, &failingRestorer{}, model)

	if _, err := runner.Run(context.Background(), "id-1", demoOriginal); err == nil {
		t.Fatal("Run succeeded, want masking error")
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 after masking error", model.calls)
	}
}

// TestModelErrorNoRestore verifies that a model error aborts the cycle before
// restore.
func TestModelErrorNoRestore(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")
	runner := NewRunner(client, client, failingModel{})

	if _, err := runner.Run(context.Background(), "id-1", demoOriginal); err == nil {
		t.Fatal("Run succeeded, want model error")
	} else if !strings.Contains(err.Error(), "model") {
		t.Fatalf("err = %v, want model error", err)
	}
}

// TestMissingCorrespondence verifies that restoring an unknown ID returns an
// error and reveals nothing.
func TestMissingCorrespondence(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")
	if _, err := client.Restore(context.Background(), "missing", "[PII_0]"); err == nil {
		t.Fatal("restore missing ID succeeded, want error")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want 404", err)
	}
}

// TestMarkerLikeInputExactCycle verifies that input text that itself looks like
// a marker passes the exact mask/restore cycle without collisions.
func TestMarkerLikeInputExactCycle(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")

	// The original contains a literal marker-like token plus a real email. The
	// masker must choose markers that do not collide with the literal token.
	original := "текст [PII_0] и mail a@b.ru"
	masked, err := client.Mask(context.Background(), "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if !strings.Contains(masked, "[PII_0]") {
		t.Fatalf("masked %q lost the literal marker-like token", masked)
	}
	restored, err := client.Restore(context.Background(), "id-1", masked)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != original {
		t.Fatalf("restored = %q, want %q", restored, original)
	}
}

// TestNoRecursiveMarkerInterpretation verifies that restored values are not
// re-interpreted as new markers.
func TestNoRecursiveMarkerInterpretation(t *testing.T) {
	base := testServer(t, []app.Consumer{demoConsumer("demo")}, map[string]string{"demo-secret": "demo"})
	client := NewClient(base, "demo-secret")

	// The original contains a literal marker-like token. After masking, the
	// literal token stays in the mask. Restoring must not substitute inside the
	// restored value.
	original := "значение [PII_0] и mail a@b.ru"
	masked, err := client.Mask(context.Background(), "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	restored, err := client.Restore(context.Background(), "id-1", masked)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != original {
		t.Fatalf("restored = %q, want %q", restored, original)
	}
}

// failingMasker returns an error for every Mask call.
type failingMasker struct{}

func (failingMasker) Mask(context.Context, string, string) (string, error) {
	return "", errors.New("masking failure")
}

// failingRestorer returns an error for every Restore call.
type failingRestorer struct{}

func (failingRestorer) Restore(context.Context, string, string) (string, error) {
	return "", errors.New("restore failure")
}
