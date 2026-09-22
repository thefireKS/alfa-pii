package recognizer

import (
	"errors"
	"testing"
)

func TestResolveDedupesIdentical(t *testing.T) {
	frags := []Fragment{
		{Type: Passport, Start: 0, End: 10},
		{Type: DriverLicense, Start: 0, End: 10},
	}
	got, err := Resolve("4506123456", frags)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d fragments, want 1", len(got))
	}
	if got[0].Start != 0 || got[0].End != 10 {
		t.Fatalf("range = [%d,%d), want [0,10)", got[0].Start, got[0].End)
	}
}

func TestResolveKeepsOuterForNested(t *testing.T) {
	// A card number [0,16) with a nested PIN [0,4): the outer card wins.
	frags := []Fragment{
		{Type: PIN, Start: 0, End: 4},
		{Type: Card, Start: 0, End: 16},
	}
	got, err := Resolve("4111111111111111", frags)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d fragments, want 1", len(got))
	}
	if got[0].Type != Card || got[0].Start != 0 || got[0].End != 16 {
		t.Fatalf("got %+v, want card [0,16)", got[0])
	}
}

func TestResolvePartialOverlapKeepsHigherPriority(t *testing.T) {
	// PIN [0,4) partially overlaps card [2,18): the union [0,18) must be
	// masked so no sensitive byte of either fragment leaks.
	frags := []Fragment{
		{Type: PIN, Start: 0, End: 4},
		{Type: Card, Start: 2, End: 18},
	}
	got, err := Resolve("xx4111111111111111", frags)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d fragments, want 1", len(got))
	}
	if got[0].Type != Card || got[0].Start != 0 || got[0].End != 18 {
		t.Fatalf("got %+v, want card [0,18)", got[0])
	}
}

func TestResolvePartialOverlapLowerPriorityTail(t *testing.T) {
	// Card [2,18) partially overlaps PIN [16,20): the union [2,20) must be
	// masked so the PIN tail [18,20) does not leak.
	frags := []Fragment{
		{Type: Card, Start: 2, End: 18},
		{Type: PIN, Start: 16, End: 20},
	}
	got, err := Resolve("xx4111111111111111 1234", frags)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d fragments, want 1", len(got))
	}
	if got[0].Start != 2 || got[0].End != 20 {
		t.Fatalf("got %+v, want [2,20)", got[0])
	}
}

func TestResolveDoesNotExtendToWholeString(t *testing.T) {
	// Two disjoint ranges must stay disjoint, not merge into one big range.
	frags := []Fragment{
		{Type: Email, Start: 0, End: 6},
		{Type: Phone, Start: 10, End: 21},
	}
	got, err := Resolve("a@b.ru +7 912 345-67-89", frags)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d fragments, want 2", len(got))
	}
}

func TestResolveInvalidRange(t *testing.T) {
	tests := []struct {
		name  string
		frags []Fragment
	}{
		{name: "negative start", frags: []Fragment{{Type: Email, Start: -1, End: 5}}},
		{name: "end beyond text", frags: []Fragment{{Type: Email, Start: 0, End: 100}}},
		{name: "inverted", frags: []Fragment{{Type: Email, Start: 5, End: 2}}},
		{name: "not rune boundary", frags: []Fragment{{Type: Email, Start: 0, End: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve("привет", tt.frags)
			if !errors.Is(err, ErrProtection) {
				t.Fatalf("err = %v, want ErrProtection", err)
			}
		})
	}
}

func TestResolveEmpty(t *testing.T) {
	got, err := Resolve("text", nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d fragments, want 0", len(got))
	}
}
