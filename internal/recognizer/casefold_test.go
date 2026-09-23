package recognizer

import (
	"strings"
	"testing"
	"unicode/utf8"

	"alfa-hackathon.local/pii/internal/masker"
)

// allTypes lists every built-in data type so a test can run the full
// recognizer set the way the service does.
var allTypes = []Type{
	Email, Phone, INN, Card, Passport, DepartmentCode, DriverLicense,
	PIN, CVV, FullName, BirthDate, BirthPlace, Citizenship,
	PassportAuthority, PassportIssueDate, Address, CardHolderName,
}

// maskAndRestore runs the full recognizer set on text, masks the resolved
// fragments and restores them, returning the masked text and the restored text.
func maskAndRestore(t *testing.T, text string) (masked, restored string) {
	t.Helper()
	reg := NewRegistry()
	recs, priority, err := reg.Recognizers(allTypes)
	if err != nil {
		t.Fatalf("Recognizers: %v", err)
	}
	var frags []Fragment
	for _, rec := range recs {
		fs, err := rec.Find(text)
		if err != nil {
			t.Fatalf("%s.Find: %v", rec.Type(), err)
		}
		frags = append(frags, fs...)
	}
	resolved, err := ResolveWithPriority(text, frags, priority)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ranges := make([]masker.Range, 0, len(resolved))
	for _, f := range resolved {
		ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
	}
	m := masker.New("PII")
	masked, table := m.Mask(text, ranges)
	restored = masker.Restore(masked, table)
	return masked, restored
}

// TestCasefoldOffsetMapping guards the fix that translated offsets found in a
// lowercased copy of the text back to the original text. strings.ToLower can
// change the byte length of a rune (for example U+212A KELVIN SIGN becomes
// 'k'), so offsets found in the lowercased text must not be used to slice the
// original text directly. The confirmed failure was "K гражданство: РФ"
// masking ": Р" instead of "РФ".
func TestCasefoldOffsetMapping(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantMask string
	}{
		{
			name:     "kelvin before value",
			in:       "K гражданство: РФ",
			wantMask: "K гражданство: [PII_0]",
		},
		{
			name:     "dotted capital i before value",
			in:       "İ гражданство: РФ",
			wantMask: "İ гражданство: [PII_0]",
		},
		{
			name:     "cyrillic value",
			in:       "гражданство: Российская Федерация",
			wantMask: "гражданство: [PII_0]",
		},
		{
			name:     "emoji before value",
			in:       "😀 гражданство: РФ",
			wantMask: "😀 гражданство: [PII_0]",
		},
		{
			name:     "non-breaking space before value",
			in:       "гражданство:\u00A0РФ",
			wantMask: "гражданство:\u00A0[PII_0]",
		},
		{
			name:     "hyphenated value",
			in:       "место рождения: г. Санкт-Петербург",
			wantMask: "место рождения: [PII_0]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked, restored := maskAndRestore(t, tt.in)
			if masked != tt.wantMask {
				t.Fatalf("masked = %q, want %q", masked, tt.wantMask)
			}
			if restored != tt.in {
				t.Fatalf("restored = %q, want %q", restored, tt.in)
			}
			if !utf8.ValidString(masked) {
				t.Fatalf("masked text is not valid UTF-8: %q", masked)
			}
		})
	}
}

// TestCasefoldKelvinPrefixPreserved checks that the prefix before the masked
// value is preserved byte-for-byte, including the multi-byte Kelvin sign.
func TestCasefoldKelvinPrefixPreserved(t *testing.T) {
	in := "K гражданство: РФ"
	masked, _ := maskAndRestore(t, in)
	prefix := "K гражданство: "
	if !strings.HasPrefix(masked, prefix) {
		t.Fatalf("masked %q does not preserve prefix %q", masked, prefix)
	}
	if strings.Contains(masked, "РФ") {
		t.Fatalf("masked %q still contains the sensitive value", masked)
	}
}

// TestCasefoldLabelOffsets checks the label matcher directly: the returned
// offsets must point into the original text even when a preceding rune changes
// byte length under lowercasing.
func TestCasefoldLabelOffsets(t *testing.T) {
	text := "K гражданство: РФ"
	matches := findLabelMatches(text, citizenshipLabels)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	after := matches[0].after
	if text[after:] != ": РФ" {
		t.Fatalf("after = %d, text[after:] = %q, want %q", after, text[after:], ": РФ")
	}
}

// TestCasefoldDictOffsets checks the dictionary matcher directly: the returned
// ranges must point into the original text even when a preceding rune changes
// byte length under lowercasing.
func TestCasefoldDictOffsets(t *testing.T) {
	text := "İ гражданство: РФ"
	ranges := findDictRanges(text, citizenshipValues)
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d, want 1", len(ranges))
	}
	got := text[ranges[0][0]:ranges[0][1]]
	if got != "РФ" {
		t.Fatalf("range %v = %q, want %q", ranges[0], got, "РФ")
	}
}

// TestCasefoldWindowBoundary checks that a context window that would otherwise
// cut a multi-byte rune is snapped to a rune boundary, so a value near the
// window edge is still recognized and masked.
func TestCasefoldWindowBoundary(t *testing.T) {
	// A long prefix of multi-byte runes pushes the value close to the start of
	// the context window; the window must not cut the value mid-rune.
	prefix := strings.Repeat("абвгд ", 12)
	in := prefix + "гражданство: РФ"
	masked, restored := maskAndRestore(t, in)
	if !strings.HasSuffix(masked, "гражданство: [PII_0]") {
		t.Fatalf("masked %q does not end with the masked value", masked)
	}
	if restored != in {
		t.Fatalf("restored != original")
	}
	if !utf8.ValidString(masked) {
		t.Fatalf("masked text is not valid UTF-8")
	}
}
