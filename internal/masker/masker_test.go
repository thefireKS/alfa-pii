package masker

import (
	"strings"
	"testing"
)

func TestMaskAndRestore(t *testing.T) {
	m := New("PII")
	tests := []struct {
		name   string
		text   string
		ranges []Range
	}{
		{name: "no ranges", text: "plain text", ranges: nil},
		{name: "single", text: "mail a@b.ru end", ranges: []Range{{Start: 5, End: 11}}},
		{name: "multiple", text: "a@b.ru and c@d.io", ranges: []Range{{Start: 0, End: 6}, {Start: 11, End: 17}}},
		{name: "cyrillic", text: "почта a@b.ru конец", ranges: []Range{{Start: len("почта "), End: len("почта a@b.ru")}}},
		{name: "emoji", text: "привет 👋 a@b.ru", ranges: []Range{{Start: len("привет 👋 "), End: len("привет 👋 a@b.ru")}}},
		{name: "overlapping", text: "abc@def.gh", ranges: []Range{{Start: 0, End: 6}, {Start: 3, End: 10}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked, table := m.Mask(tt.text, tt.ranges)
			restored := Restore(masked, table)
			if restored != tt.text {
				t.Fatalf("restore = %q, want %q (masked=%q)", restored, tt.text, masked)
			}
			// Masked text must not contain any original fragment.
			for _, r := range tt.ranges {
				if strings.Contains(masked, tt.text[r.Start:r.End]) {
					t.Fatalf("masked %q still contains fragment %q", masked, tt.text[r.Start:r.End])
				}
			}
		})
	}
}

func TestMaskMarkersDoNotConflictWithExistingText(t *testing.T) {
	m := New("PII")
	// The text already contains [PII_0], so the generated marker must skip it.
	text := "[PII_0] a@b.ru"
	ranges := []Range{{Start: len("[PII_0] "), End: len("[PII_0] a@b.ru")}}
	masked, table := m.Mask(text, ranges)
	if len(table) != 1 {
		t.Fatalf("got %d replacements, want 1", len(table))
	}
	if table[0].Marker == "[PII_0]" {
		t.Fatalf("marker %q collides with existing text", table[0].Marker)
	}
	if strings.Contains(masked, "a@b.ru") {
		t.Fatalf("masked %q still contains the email", masked)
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q", got, text)
	}
}

func TestRestoreLeavesUnknownMarkers(t *testing.T) {
	table := []Replacement{{Marker: "[PII_0]", Original: "a@b.ru"}}
	got := Restore("keep [PII_0] and [UNKNOWN]", table)
	want := "keep a@b.ru and [UNKNOWN]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
