package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

func newTestService() *Service {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	return New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
}

// allRecognizers returns one recognizer per supported category.
func allRecognizers() []Recognizer {
	return []Recognizer{
		recognizer.EmailRecognizer{},
		recognizer.PhoneRecognizer{},
		recognizer.INNRecognizer{},
		recognizer.CardRecognizer{},
		recognizer.PassportRecognizer{},
		recognizer.DepartmentCodeRecognizer{},
		recognizer.DriverLicenseRecognizer{},
		recognizer.PINRecognizer{},
		recognizer.CVVRecognizer{},
	}
}

func newAllService() *Service {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	return New(allRecognizers(), st, masker.New("PII"))
}

// TestProcessAllCategories verifies the full mask -> restore cycle for every
// supported category, including repeats and exact restoration.
func TestProcessAllCategories(t *testing.T) {
	svc := newAllService()
	ctx := context.Background()
	tests := []struct {
		name     string
		original string
		leak     string
	}{
		{name: "email", original: "почта a.b@example.com", leak: "a.b@example.com"},
		{name: "phone", original: "тел +7 (912) 345-67-89", leak: "+7 (912) 345-67-89"},
		{name: "inn", original: "ИНН 7707083893", leak: "7707083893"},
		{name: "card", original: "карта 4111 1111 1111 1111", leak: "4111 1111 1111 1111"},
		{name: "passport", original: "паспорт 4506 123456", leak: "4506 123456"},
		{name: "department", original: "код подразделения 770-123", leak: "770-123"},
		{name: "driver license", original: "водительское удостоверение 7701 123456", leak: "7701 123456"},
		{name: "pin", original: "ПИН 1234", leak: "1234"},
		{name: "cvv", original: "CVV 123", leak: "123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "id-" + tt.name
			res, err := svc.Process(ctx, id, tt.original)
			if err != nil {
				t.Fatalf("mask: %v", err)
			}
			masked := res.Text
			if masked == tt.original {
				t.Fatalf("masked text equals original")
			}
			if contains(masked, tt.leak) {
				t.Fatalf("masked %q still contains %q", masked, tt.leak)
			}
			// Repeat of the original returns the same mask.
			res2, err := svc.Process(ctx, id, tt.original)
			if err != nil {
				t.Fatalf("repeat original: %v", err)
			}
			if res2.Text != masked {
				t.Fatalf("repeat original = %q, want %q", res2.Text, masked)
			}
			// Repeat of the mask returns the exact original.
			res3, err := svc.Process(ctx, id, masked)
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			if res3.Text != tt.original {
				t.Fatalf("restore = %q, want %q", res3.Text, tt.original)
			}
		})
	}
}

// TestProcessNoFalsePositives verifies that order numbers, amounts and dates
// are not masked just for matching length.
func TestProcessNoFalsePositives(t *testing.T) {
	svc := newAllService()
	ctx := context.Background()
	tests := []struct {
		name string
		in   string
	}{
		{name: "order number", in: "заказ 7707083893"},
		{name: "amount", in: "сумма 4111111111111111"},
		{name: "date", in: "дата 4506123456"},
		{name: "bare pin", in: "1234"},
		{name: "bare cvv", in: "123"},
		{name: "plain text", in: "просто текст без данных"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := svc.Process(ctx, "id-nfp-"+tt.name, tt.in)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			if res.Text != tt.in {
				t.Fatalf("got %q, want %q (should not be masked)", res.Text, tt.in)
			}
		})
	}
}

func TestProcessMaskThenRestore(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	original := "reach me at a.b@example.com today"

	res, err := svc.Process(ctx, "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	masked := res.Text
	if masked == original {
		t.Fatal("masked text equals original")
	}
	if contains(masked, "a.b@example.com") {
		t.Fatalf("masked text still contains email: %q", masked)
	}

	// Repeat of the original returns the same mask.
	res2, err := svc.Process(ctx, "id-1", original)
	if err != nil {
		t.Fatalf("repeat original: %v", err)
	}
	if res2.Text != masked {
		t.Fatalf("repeat original = %q, want %q", res2.Text, masked)
	}

	// Repeat of the mask returns the exact original.
	res3, err := svc.Process(ctx, "id-1", masked)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res3.Text != original {
		t.Fatalf("restore = %q, want %q", res3.Text, original)
	}
}

func TestProcessNoPII(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	text := "просто текст без данных"
	res, err := svc.Process(ctx, "id-nopii", text)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.Text != text {
		t.Fatalf("got %q, want %q", res.Text, text)
	}
	// Repeat returns the same text.
	res2, _ := svc.Process(ctx, "id-nopii", text)
	if res2.Text != text {
		t.Fatalf("repeat = %q, want %q", res2.Text, text)
	}
}

func TestProcessEmptyPayload(t *testing.T) {
	svc := newTestService()
	res, err := svc.Process(context.Background(), "id-empty", "")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.Text != "" {
		t.Fatalf("got %q, want empty", res.Text)
	}
}

func TestProcessConflict(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	_, _ = svc.Process(ctx, "id-c", "a@b.ru")
	_, err := svc.Process(ctx, "id-c", "unrelated text")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestProcessConcurrentSameID(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	original := "mail a@b.ru"
	const n = 50
	var wg sync.WaitGroup
	masks := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.Process(ctx, "id-race", original)
			masks[i] = res.Text
			errs[i] = err
		}(i)
	}
	wg.Wait()
	// All must succeed and produce the same mask.
	first := masks[0]
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if masks[i] != first {
			t.Fatalf("goroutine %d mask = %q, want %q", i, masks[i], first)
		}
	}
	// Restore still works.
	res, err := svc.Process(ctx, "id-race", first)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Text != original {
		t.Fatalf("restore = %q, want %q", res.Text, original)
	}
}

func TestProcessCapacity(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	svc := New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	ctx := context.Background()
	_, _ = svc.Process(ctx, "id-1", "a@b.ru")
	_, err := svc.Process(ctx, "id-2", "c@d.io")
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
	}
}

// TestProcessLostResponse verifies that if the client loses the response after
// a successful mask, a retry with the original returns the saved mask.
func TestProcessLostResponse(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	original := "mail a@b.ru"
	res, err := svc.Process(ctx, "id-lost", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	// Client never saw the response; retry with the original.
	res2, err := svc.Process(ctx, "id-lost", original)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res2.Text != res.Text {
		t.Fatalf("retry = %q, want %q", res2.Text, res.Text)
	}
}

// TestProcessRepeatUsesSavedMaskAfterRuleChange verifies that a ready record
// keeps its saved mask even if recognition rules change.
func TestProcessRepeatUsesSavedMaskAfterRuleChange(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	svc := New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	ctx := context.Background()
	original := "mail a@b.ru"
	res, err := svc.Process(ctx, "id-rule", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	// Replace the recognizer set with one that recognizes nothing.
	svc2 := New(nil, st, masker.New("PII"))
	res2, err := svc2.Process(ctx, "id-rule", original)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if res2.Text != res.Text {
		t.Fatalf("repeat = %q, want saved mask %q", res2.Text, res.Text)
	}
}

// TestProcessOriginalEqualsMask verifies the no-PII case where the original and
// the mask are identical, and that a different text for the same key conflicts.
func TestProcessOriginalEqualsMask(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	text := "просто текст"
	res, err := svc.Process(ctx, "id-oeq", text)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.Text != text {
		t.Fatalf("got %q, want %q", res.Text, text)
	}
	// Repeat returns the same text.
	res2, err := svc.Process(ctx, "id-oeq", text)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if res2.Text != text {
		t.Fatalf("repeat = %q, want %q", res2.Text, text)
	}
	// A different text for the same key conflicts.
	_, err = svc.Process(ctx, "id-oeq", "другой текст")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// TestProcessCreatorCancelledNoPartialState verifies that cancelling a request
// that would create a new correspondence leaves no partial state and a later
// request succeeds.
func TestProcessCreatorCancelledNoPartialState(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	svc := New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Process(ctx, "id-cancel", "a@b.ru")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	// No partial state: a fresh request succeeds.
	res, err := svc.Process(context.Background(), "id-cancel", "a@b.ru")
	if err != nil {
		t.Fatalf("process after cancel: %v", err)
	}
	if res.Text == "a@b.ru" {
		t.Fatalf("masked text equals original: %q", res.Text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
