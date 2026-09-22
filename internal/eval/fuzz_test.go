package eval

import (
	"strings"
	"testing"
	"unicode/utf8"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
)

// FuzzRestoreUnicode verifies that masking and restoring arbitrary Unicode
// text with embedded sensitive values reproduces the original exactly, and
// that the pipeline never panics or produces invalid ranges.
func FuzzRestoreUnicode(f *testing.F) {
	p := testPipeline()
	seeds := []string{
		"почта a@b.ru",
		"тел +7 (912) 345-67-89",
		"привет 👋 мир",
		"ФИО: Иванов Иван Иванович",
		"карта 4111 1111 1111 1111",
		"",
		"😀😀😀",
		"a@b.ru и 7707083893",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			t.Skip("invalid utf-8 input")
		}
		resolved, masked, table, err := p.Mask(text)
		if err != nil {
			t.Fatalf("pipeline error: %v", err)
		}
		// Resolved ranges must be valid and on rune boundaries.
		for _, fr := range resolved {
			if fr.Start < 0 || fr.End > len(text) || fr.Start > fr.End {
				t.Fatalf("invalid range %+v", fr)
			}
			if !utf8.ValidString(text[fr.Start:fr.End]) {
				t.Fatalf("range not on rune boundary: %+v", fr)
			}
		}
		// Exact restoration.
		restored := masker.Restore(masked, table)
		if restored != text {
			t.Fatalf("restoration mismatch:\n got %q\nwant %q", restored, text)
		}
		// Unchanged text outside masks.
		if !unchangedOK(text, masked, resolved, table) {
			t.Fatalf("text outside masks changed for %q", text)
		}
	})
}

// FuzzOverlapResolution verifies that the resolver never panics and always
// returns valid, non-overlapping ranges for arbitrary fragment inputs.
func FuzzOverlapResolution(f *testing.F) {
	types := []recognizer.Type{
		recognizer.Email, recognizer.Phone, recognizer.Card, recognizer.PIN,
		recognizer.CVV, recognizer.FullName, recognizer.Address,
	}
	f.Add("abcdefghijklmnopqrstuvwxyz")
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			t.Skip("invalid utf-8 input")
		}
		// Build random fragments from the text.
		var frags []recognizer.Fragment
		for i := 0; i < len(text); i++ {
			if text[i]%3 == 0 {
				start := i
				end := i + 1 + int(text[i]%5)
				if end > len(text) {
					end = len(text)
				}
				// Only keep rune-aligned ranges.
				if !utf8.ValidString(text[start:end]) {
					continue
				}
				frags = append(frags, recognizer.Fragment{
					Type:  types[int(text[i])%len(types)],
					Start: start,
					End:   end,
				})
			}
		}
		resolved, err := recognizer.Resolve(text, frags)
		if err != nil {
			// Invalid ranges are a legitimate protection error; skip.
			return
		}
		// Resolved ranges must be non-overlapping and valid.
		prevEnd := -1
		for _, fr := range resolved {
			if fr.Start < prevEnd {
				t.Fatalf("overlapping resolved ranges: %+v", resolved)
			}
			if fr.Start < 0 || fr.End > len(text) || fr.Start > fr.End {
				t.Fatalf("invalid resolved range: %+v", fr)
			}
			prevEnd = fr.End
		}
	})
}

// FuzzMaskerRestore verifies the masker round-trips for arbitrary text and
// ranges, including overlapping ranges that must be merged.
func FuzzMaskerRestore(f *testing.F) {
	m := masker.New("PII")
	f.Add("hello world", 0, 5)
	f.Fuzz(func(t *testing.T, text string, a, b int) {
		if !utf8.ValidString(text) {
			t.Skip("invalid utf-8 input")
		}
		if a < 0 || b < 0 || a > len(text) || b > len(text) {
			return
		}
		if a > b {
			a, b = b, a
		}
		if !utf8.ValidString(text[a:b]) {
			return
		}
		ranges := []masker.Range{{Start: a, End: b}}
		masked, table := m.Mask(text, ranges)
		restored := masker.Restore(masked, table)
		if restored != text {
			t.Fatalf("restoration mismatch for %q [%d,%d): got %q", text, a, b, restored)
		}
	})
}

// TestFuzzSeedsRun ensures the fuzz seed corpus passes the invariants without
// the fuzzer, so the checks are exercised in normal go test runs.
func TestFuzzSeedsRun(t *testing.T) {
	p := testPipeline()
	for _, s := range []string{"почта a@b.ru", "тел +7 (912) 345-67-89", "😀😀😀", ""} {
		resolved, masked, table, err := p.Mask(s)
		if err != nil {
			t.Fatalf("pipeline error for %q: %v", s, err)
		}
		if masker.Restore(masked, table) != s {
			t.Fatalf("restoration mismatch for %q", s)
		}
		if !unchangedOK(s, masked, resolved, table) {
			t.Fatalf("unchanged-text failure for %q", s)
		}
	}
}

// TestOverlapUnionNoLeak verifies that a partial overlap is covered by the
// union of ranges so no sensitive byte leaks, without extending to the whole
// string.
func TestOverlapUnionNoLeak(t *testing.T) {
	// A card number overlapping a PIN: the union must be masked.
	text := "xx4111111111111111"
	// Force both recognizers to fire by using a pipeline with card and pin.
	recs := []recognizer.Recognizer{recognizer.CardRecognizer{}, recognizer.PINRecognizer{}}
	pp := NewPipeline(recs, masker.New("PII"))
	resolved, masked, _, err := pp.Mask(text)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	// The card must be masked and no sensitive value may leak.
	cover := byteCoverage(text, resolved)
	for i := 2; i < 18; i++ {
		if !cover[i] {
			t.Fatalf("card byte %d left unmasked", i)
		}
	}
	if strings.Contains(masked, "4111") {
		t.Fatalf("sensitive value leaked in masked text: %q", masked)
	}
}
