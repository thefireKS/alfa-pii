package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

func newManagedService(consumers []Consumer) (*Service, error) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	processTypes := []recognizer.Type{recognizer.Email, recognizer.Phone}
	return NewManaged(reg, processTypes, consumers, st, st, masker.New("PII"))
}

func emailConsumer(name string) Consumer {
	return Consumer{
		Name:           name,
		Enabled:        true,
		Types:          []recognizer.Type{recognizer.Email},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     FormatMarker,
	}
}

func TestMaskRestoreIsolationBetweenConsumers(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha"), emailConsumer("beta")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	ctx := context.Background()
	originalA := "mail a@b.ru"
	originalB := "mail c@d.io"

	// Both consumers mask the same payload_id independently with different
	// originals; beta's record does not conflict with alpha's.
	ra, err := svc.Mask(ctx, "alpha", "id-1", originalA)
	if err != nil {
		t.Fatalf("alpha mask: %v", err)
	}
	rb, err := svc.Mask(ctx, "beta", "id-1", originalB)
	if err != nil {
		t.Fatalf("beta mask: %v", err)
	}
	if ra.Text == originalA || rb.Text == originalB {
		t.Fatalf("masks leaked original: %q %q", ra.Text, rb.Text)
	}

	// Beta restoring alpha's mask must not recover alpha's original.
	rr, err := svc.Restore(ctx, "beta", "id-1", ra.Text)
	if err != nil {
		t.Fatalf("beta restore alpha mask: %v", err)
	}
	if rr.Text == originalA {
		t.Fatalf("beta recovered alpha original: %q", rr.Text)
	}
	// Beta restoring its own mask returns its own original.
	rr2, err := svc.Restore(ctx, "beta", "id-1", rb.Text)
	if err != nil {
		t.Fatalf("beta restore own mask: %v", err)
	}
	if rr2.Text != originalB {
		t.Fatalf("beta restore own = %q, want %q", rr2.Text, originalB)
	}
}

func TestMaskNeverDemasks(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	ctx := context.Background()
	original := "mail a@b.ru"
	ra, err := svc.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	// Passing the mask to /v1/mask must return the mask, not the original.
	ra2, err := svc.Mask(ctx, "alpha", "id-1", ra.Text)
	if err != nil {
		t.Fatalf("mask of mask: %v", err)
	}
	if ra2.Text != ra.Text {
		t.Fatalf("mask of mask = %q, want %q (must not demask)", ra2.Text, ra.Text)
	}
}

func TestRestoreSubstitutesMarkers(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	ctx := context.Background()
	original := "a@b.ru and c@d.io"
	ra, err := svc.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if !strings.Contains(ra.Text, "[PII_0]") || !strings.Contains(ra.Text, "[PII_1]") {
		t.Fatalf("mask = %q, want markers", ra.Text)
	}
	// Restore a reordered text with the consumer's own markers.
	reordered := "[PII_1] then [PII_0]"
	rr, err := svc.Restore(ctx, "alpha", "id-1", reordered)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rr.Text != "c@d.io then a@b.ru" {
		t.Fatalf("restore = %q, want %q", rr.Text, "c@d.io then a@b.ru")
	}
}

func TestRestoreStarsExactOnly(t *testing.T) {
	c := emailConsumer("alpha")
	c.MaskFormat = FormatStars
	svc, err := newManagedService([]Consumer{c})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	ctx := context.Background()
	original := "mail a@b.ru"
	ra, err := svc.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if !strings.Contains(ra.Text, "****") {
		t.Fatalf("mask = %q, want stars", ra.Text)
	}
	// Exact restore of the returned mask works.
	rr, err := svc.Restore(ctx, "alpha", "id-1", ra.Text)
	if err != nil {
		t.Fatalf("exact restore: %v", err)
	}
	if rr.Text != original {
		t.Fatalf("restore = %q, want %q", rr.Text, original)
	}
	// A modified text with stars is ambiguous and must be rejected.
	if _, err := svc.Restore(ctx, "alpha", "id-1", "mail **** today"); !errors.Is(err, ErrConflict) {
		t.Fatalf("modified stars restore err = %v, want ErrConflict", err)
	}
}

func TestConsumerPolicyGates(t *testing.T) {
	ctx := context.Background()

	disabled := emailConsumer("off")
	disabled.Enabled = false
	noMask := emailConsumer("nomask")
	noMask.MaskingEnabled = false
	noRestore := emailConsumer("norestore")
	noRestore.CanRestore = false

	svc, err := newManagedService([]Consumer{disabled, noMask, noRestore})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}

	if _, err := svc.Mask(ctx, "off", "id", "a@b.ru"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled mask err = %v, want ErrDisabled", err)
	}
	if _, err := svc.Restore(ctx, "off", "id", "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled restore err = %v, want ErrDisabled", err)
	}
	if _, err := svc.Mask(ctx, "nomask", "id", "a@b.ru"); !errors.Is(err, ErrMaskingDisabled) {
		t.Fatalf("no-mask err = %v, want ErrMaskingDisabled", err)
	}
	// noRestore can mask but not restore.
	if _, err := svc.Mask(ctx, "norestore", "id", "a@b.ru"); err != nil {
		t.Fatalf("norestore mask: %v", err)
	}
	if _, err := svc.Restore(ctx, "norestore", "id", "x"); !errors.Is(err, ErrRestoreForbidden) {
		t.Fatalf("norestore restore err = %v, want ErrRestoreForbidden", err)
	}
}

func TestUnknownConsumer(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	if _, err := svc.Mask(context.Background(), "ghost", "id", "a@b.ru"); !errors.Is(err, ErrUnknownConsumer) {
		t.Fatalf("err = %v, want ErrUnknownConsumer", err)
	}
}

func TestRestoreNotFound(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	if _, err := svc.Restore(context.Background(), "alpha", "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRepeatUsesSavedRecordAfterSettingsChange(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	processTypes := []recognizer.Type{recognizer.Email}
	alpha := emailConsumer("alpha")
	svc, err := NewManaged(reg, processTypes, []Consumer{alpha}, st, st, masker.New("PII"))
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	ctx := context.Background()
	original := "mail a@b.ru"
	ra, err := svc.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}

	// Change the consumer's types to nothing, but keep access rights. A repeat
	// must use the saved record, not re-recognize.
	changed := alpha
	changed.Types = nil
	svc2, err := NewManaged(reg, processTypes, []Consumer{changed}, st, st, masker.New("PII"))
	if err != nil {
		t.Fatalf("NewManaged changed: %v", err)
	}
	ra2, err := svc2.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("repeat mask: %v", err)
	}
	if ra2.Text != ra.Text {
		t.Fatalf("repeat mask = %q, want saved %q", ra2.Text, ra.Text)
	}
	rr, err := svc2.Restore(ctx, "alpha", "id-1", ra.Text)
	if err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if rr.Text != original {
		t.Fatalf("repeat restore = %q, want %q", rr.Text, original)
	}
}

func TestMaskConflict(t *testing.T) {
	svc, err := newManagedService([]Consumer{emailConsumer("alpha")})
	if err != nil {
		t.Fatalf("newManagedService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.Mask(ctx, "alpha", "id-1", "a@b.ru"); err != nil {
		t.Fatalf("mask: %v", err)
	}
	if _, err := svc.Mask(ctx, "alpha", "id-1", "unrelated"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// TestRegexpRuleTypeConnected verifies that a new data type added through the
// registry and configuration masks and restores without new Go code.
func TestRegexpRuleTypeConnected(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	if err := reg.AddRegexpRule("contract_number", 65, `договор №[0-9]{6}`, 10); err != nil {
		t.Fatalf("AddRegexpRule: %v", err)
	}
	c := Consumer{
		Name:           "alpha",
		Enabled:        true,
		Types:          []recognizer.Type{"contract_number"},
		MaskingEnabled: true,
		CanRestore:     true,
		MaskFormat:     FormatMarker,
	}
	svc, err := NewManaged(reg, []recognizer.Type{recognizer.Email}, []Consumer{c}, st, st, masker.New("PII"))
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	ctx := context.Background()
	original := "договор №123456"
	ra, err := svc.Mask(ctx, "alpha", "id-1", original)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	if ra.Text == original || strings.Contains(ra.Text, "123456") {
		t.Fatalf("masked = %q", ra.Text)
	}
	rr, err := svc.Restore(ctx, "alpha", "id-1", ra.Text)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rr.Text != original {
		t.Fatalf("restore = %q, want %q", rr.Text, original)
	}
}

// TestNewManagedRejectsUnknownConsumerType verifies that an invalid consumer
// configuration is rejected wholly at construction.
func TestNewManagedRejectsUnknownConsumerType(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	c := emailConsumer("alpha")
	c.Types = []recognizer.Type{"not_a_real_type"}
	if _, err := NewManaged(reg, []recognizer.Type{recognizer.Email}, []Consumer{c}, st, st, masker.New("PII")); err == nil {
		t.Fatal("expected error for unknown consumer type")
	}
}

// TestNewManagedRejectsReservedScopeName verifies that a consumer name that
// collides with the reserved /process scope is rejected at construction, so an
// authenticated consumer can never alias the public scope's store key.
func TestNewManagedRejectsReservedScopeName(t *testing.T) {
	st := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	for _, name := range []string{ConsumerScope, "alpha:beta"} {
		c := emailConsumer(name)
		if _, err := NewManaged(reg, []recognizer.Type{recognizer.Email}, []Consumer{c}, st, st, masker.New("PII")); err == nil {
			t.Fatalf("expected error for consumer name %q", name)
		}
	}
}

// TestProcessAndConsumerStoresIsolated verifies that exhausting the /process
// store does not cause capacity errors for managed consumers, because the two
// scopes use separate stores.
func TestProcessAndConsumerStoresIsolated(t *testing.T) {
	processStore := store.NewMemory(store.Limits{MaxEntries: 1, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	consumerStore := store.NewMemory(store.Limits{MaxEntries: 100, MaxBytes: 1 << 20, MaxRecordBytes: 1 << 20, TTL: time.Hour, CreateWait: time.Second})
	reg := recognizer.NewRegistry()
	svc, err := NewManaged(reg, []recognizer.Type{recognizer.Email}, []Consumer{emailConsumer("alpha")}, processStore, consumerStore, masker.New("PII"))
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	ctx := context.Background()
	// Fill the /process store to capacity.
	if _, err := svc.Process(ctx, "p-1", "mail a@b.ru"); err != nil {
		t.Fatalf("process mask: %v", err)
	}
	if _, err := svc.Process(ctx, "p-2", "mail c@d.io"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("process second = %v, want ErrCapacity", err)
	}
	// The consumer store is separate and still accepts new records.
	if _, err := svc.Mask(ctx, "alpha", "c-1", "mail e@f.gh"); err != nil {
		t.Fatalf("consumer mask after process capacity: %v", err)
	}
}
