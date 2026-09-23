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

// TestMaskDistinctMarkersForDistinctValues verifies that different original
// values never share a marker, even when the input already contains literal
// markers that occupy low numbers.
func TestMaskDistinctMarkersForDistinctValues(t *testing.T) {
	m := New("PII")
	text := "[PII_0] email alice@example.test, второй bob@example.test"
	ranges := []Range{
		{Start: len("[PII_0] email "), End: len("[PII_0] email alice@example.test")},
		{Start: len("[PII_0] email alice@example.test, второй "), End: len(text)},
	}
	masked, table := m.Mask(text, ranges)
	if len(table) != 2 {
		t.Fatalf("got %d replacements, want 2", len(table))
	}
	if table[0].Marker == table[1].Marker {
		t.Fatalf("distinct values share marker %q", table[0].Marker)
	}
	if table[0].Marker == "[PII_0]" || table[1].Marker == "[PII_0]" {
		t.Fatalf("marker collides with existing literal marker")
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q (masked=%q)", got, text, masked)
	}
}

// TestMaskSkipsSeveralOccupiedNumbers verifies that allocation skips every
// marker number already present in the input, not just the first one.
func TestMaskSkipsSeveralOccupiedNumbers(t *testing.T) {
	m := New("PII")
	text := "[PII_0] [PII_2] [PII_5] a@b.ru"
	ranges := []Range{{Start: len("[PII_0] [PII_2] [PII_5] "), End: len(text)}}
	masked, table := m.Mask(text, ranges)
	if len(table) != 1 {
		t.Fatalf("got %d replacements, want 1", len(table))
	}
	if table[0].Marker != "[PII_1]" {
		t.Fatalf("marker = %q, want [PII_1]", table[0].Marker)
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q", got, text)
	}
}

// TestMaskRepeatedValuesRestoreExact verifies that a repeated original value
// appearing twice is restored exactly, so the repeated value is not lost.
func TestMaskRepeatedValuesRestoreExact(t *testing.T) {
	m := New("PII")
	text := "a@b.ru and again a@b.ru"
	ranges := []Range{{Start: 0, End: 6}, {Start: len("a@b.ru and again "), End: len(text)}}
	masked, table := m.Mask(text, ranges)
	if len(table) != 2 {
		t.Fatalf("got %d replacements, want 2", len(table))
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q", got, text)
	}
}

// TestMaskCyrillicAndEmojiWithLiteralMarkers verifies that cyrillic and emoji
// fragments are masked and restored exactly even when the input contains
// literal markers that occupy low numbers.
func TestMaskCyrillicAndEmojiWithLiteralMarkers(t *testing.T) {
	m := New("PII")
	text := "[PII_0] почта a@b.ru и привет 👋 c@d.io"
	ranges := []Range{
		{Start: len("[PII_0] почта "), End: len("[PII_0] почта a@b.ru")},
		{Start: len("[PII_0] почта a@b.ru и привет 👋 "), End: len(text)},
	}
	masked, table := m.Mask(text, ranges)
	if len(table) != 2 {
		t.Fatalf("got %d replacements, want 2", len(table))
	}
	if table[0].Marker == table[1].Marker {
		t.Fatalf("distinct values share marker %q", table[0].Marker)
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q (masked=%q)", got, text, masked)
	}
}

// TestRestoreDoesNotResubstituteInsertedOriginals verifies that restoration
// does not reprocess an original value that was inserted by an earlier marker
// replacement, so a value that itself looks like a marker is not substituted
// again.
func TestRestoreDoesNotResubstituteInsertedOriginals(t *testing.T) {
	table := []Replacement{
		{Marker: "[PII_0]", Original: "x [PII_1] y"},
		{Marker: "[PII_1]", Original: "inner@example.test"},
	}
	got := Restore("[PII_0]", table)
	want := "x [PII_1] y"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestMaskPreservesTextOutsideRanges verifies that literal markers and text
// outside the masked ranges are left untouched.
func TestMaskPreservesTextOutsideRanges(t *testing.T) {
	m := New("PII")
	text := "keep [PII_9] and a@b.ru end"
	ranges := []Range{{Start: len("keep [PII_9] and "), End: len("keep [PII_9] and a@b.ru")}}
	masked, table := m.Mask(text, ranges)
	if !strings.Contains(masked, "keep [PII_9] and ") || !strings.Contains(masked, " end") {
		t.Fatalf("masked %q lost text outside the range", masked)
	}
	if got := Restore(masked, table); got != text {
		t.Fatalf("restore = %q, want %q", got, text)
	}
}
