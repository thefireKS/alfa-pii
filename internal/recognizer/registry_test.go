package recognizer

import (
	"errors"
	"testing"
)

func TestRegistryBuiltinTypes(t *testing.T) {
	r := NewRegistry()
	recs, priority, err := r.Recognizers([]Type{Email, Phone})
	if err != nil {
		t.Fatalf("Recognizers: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if priority(Email) != Priority(Email) {
		t.Fatalf("priority(email) = %d, want %d", priority(Email), Priority(Email))
	}
}

func TestRegistryUnknownType(t *testing.T) {
	r := NewRegistry()
	if _, _, err := r.Recognizers([]Type{"nope"}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("err = %v, want ErrUnknownType", err)
	}
}

func TestRegistryRegexpRule(t *testing.T) {
	r := NewRegistry()
	if err := r.AddRegexpRule("contract_number", 65, `договор №[0-9]{6}`, 10); err != nil {
		t.Fatalf("AddRegexpRule: %v", err)
	}
	if !r.Known("contract_number") {
		t.Fatal("contract_number should be known")
	}
	if r.Priority("contract_number") != 65 {
		t.Fatalf("priority = %d, want 65", r.Priority("contract_number"))
	}
	recs, _, err := r.Recognizers([]Type{"contract_number"})
	if err != nil {
		t.Fatalf("Recognizers: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1", len(recs))
	}
	frags, err := recs[0].Find("договор №123456 и договор №654321")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(frags) != 2 {
		t.Fatalf("frags = %d, want 2", len(frags))
	}
	if frags[0].Type != "contract_number" {
		t.Fatalf("type = %q", frags[0].Type)
	}
}

func TestRegistryRegexpRuleMaxMatches(t *testing.T) {
	r := NewRegistry()
	if err := r.AddRegexpRule("t", 1, `x`, 2); err != nil {
		t.Fatalf("AddRegexpRule: %v", err)
	}
	recs, _, _ := r.Recognizers([]Type{"t"})
	frags, _ := recs[0].Find("x x x x")
	if len(frags) != 2 {
		t.Fatalf("frags = %d, want 2 (capped)", len(frags))
	}
}

func TestRegistryRegexpRuleRejectsInvalid(t *testing.T) {
	r := NewRegistry()
	if err := r.AddRegexpRule("email", 1, `x`, 1); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("built-in collision err = %v, want ErrInvalidRule", err)
	}
	if err := r.AddRegexpRule("t", 1, `[`, 1); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("bad pattern err = %v, want ErrInvalidRule", err)
	}
	if err := r.AddRegexpRule("t", 1, `x`, 0); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("zero max matches err = %v, want ErrInvalidRule", err)
	}
	if err := r.AddRegexpRule("", 1, `x`, 1); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("empty type err = %v, want ErrInvalidRule", err)
	}
}

func TestRegistryRegexpRulePriorityInResolution(t *testing.T) {
	r := NewRegistry()
	// A regexp rule with higher priority than email should win a partial
	// overlap. The rule matches the whole "a@b.ru" plus surrounding text.
	if err := r.AddRegexpRule("wide", 200, `[a-z]+@[a-z]+\.[a-z]+`, 10); err != nil {
		t.Fatalf("AddRegexpRule: %v", err)
	}
	recs, priority, err := r.Recognizers([]Type{Email, "wide"})
	if err != nil {
		t.Fatalf("Recognizers: %v", err)
	}
	var frags []Fragment
	for _, rec := range recs {
		fs, _ := rec.Find("a@b.ru")
		frags = append(frags, fs...)
	}
	resolved, err := ResolveWithPriority("a@b.ru", frags, priority)
	if err != nil {
		t.Fatalf("ResolveWithPriority: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Type != "wide" {
		t.Fatalf("resolved = %+v, want single wide fragment", resolved)
	}
}
