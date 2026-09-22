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
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, TTL: time.Hour})
	return New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
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
	st := store.NewMemory(store.Limits{MaxEntries: 1, MaxBytes: 1 << 20, TTL: time.Hour})
	svc := New([]Recognizer{recognizer.EmailRecognizer{}}, st, masker.New("PII"))
	ctx := context.Background()
	_, _ = svc.Process(ctx, "id-1", "a@b.ru")
	_, err := svc.Process(ctx, "id-2", "c@d.io")
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v, want ErrCapacity", err)
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
