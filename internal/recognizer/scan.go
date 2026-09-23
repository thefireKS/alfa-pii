package recognizer

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// digitSpan is a maximal run of digits and allowed structural characters.
type digitSpan struct {
	start  int
	end    int
	digits string
}

// scanSpans returns maximal spans of digits and the given structural
// characters. Structural characters are allowed only between digits, never at
// the edges of a span. The concatenated digits are returned with each span so
// recognizers can validate the normalized form while keeping byte ranges in
// the original text.
func scanSpans(text string, structural string) []digitSpan {
	var spans []digitSpan
	i := 0
	n := len(text)
	for i < n {
		for i < n && !isDigit(text[i]) && !isStructural(text[i], structural) {
			i++
		}
		if i >= n {
			break
		}
		firstDigit := -1
		var digits []byte
		lastDigit := -1
		for i < n && (isDigit(text[i]) || isStructural(text[i], structural)) {
			if isDigit(text[i]) {
				if firstDigit < 0 {
					firstDigit = i
				}
				digits = append(digits, text[i])
				lastDigit = i
			}
			i++
		}
		if firstDigit >= 0 {
			spans = append(spans, digitSpan{start: firstDigit, end: lastDigit + 1, digits: string(digits)})
		}
	}
	return spans
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isStructural(b byte, structural string) bool {
	return strings.IndexByte(structural, b) >= 0
}

// negativeContextWords suppress masking when they appear near a number, so
// order numbers, amounts and dates are not masked just for matching length.
var negativeContextWords = []string{"заказ", "order", "сумма", "amount", "дата", "date", "номер заказа", "order number"}

// hasNegativeContext reports whether a negative context word appears near the
// span, indicating the number is an order number, amount or date rather than a
// sensitive requisite.
func hasNegativeContext(text string, start, end int) bool {
	return hasContext(text, start, end, negativeContextWords)
}

// findContextSigned returns fragments of the given type for digit spans of
// exactly length that appear near one of the keywords. It is shared by
// recognizers whose short, ambiguous numbers require a context word so random
// digits are not treated as secrets. A negative context word (order number,
// amount, date) suppresses the match.
func findContextSigned(text string, structural string, length int, keywords []string, typ Type) ([]Fragment, error) {
	spans := scanSpans(text, structural)
	var frags []Fragment
	for _, sp := range spans {
		if len(sp.digits) != length {
			continue
		}
		if !hasContext(text, sp.start, sp.end, keywords) {
			continue
		}
		if hasNegativeContext(text, sp.start, sp.end) {
			continue
		}
		frags = append(frags, Fragment{Type: typ, Start: sp.start, End: sp.end})
	}
	return frags, nil
}

// snapToRuneStart returns the smallest offset >= off that is on a rune
// boundary, so a window never starts in the middle of a multi-byte rune.
func snapToRuneStart(s string, off int) int {
	for off < len(s) && !utf8.RuneStart(s[off]) {
		off++
	}
	return off
}

// snapToRuneEnd returns the largest offset <= off that is on a rune boundary,
// so a window never ends in the middle of a multi-byte rune.
func snapToRuneEnd(s string, off int) int {
	for off > 0 && off < len(s) && !utf8.RuneStart(s[off]) {
		off--
	}
	return off
}

// fieldBounds returns the byte range [lo, hi) of the field containing the span
// [start, end). A field is a segment delimited by ';' or a newline. Binding
// context to the field keeps a label in one field from leaking into a
// neighbouring field: for example the word "дата" in "Дата выдачи: 01.01.2020;
// паспорт 4510 123456" must not suppress the passport in the second field.
func fieldBounds(text string, start, end int) (int, int) {
	lo := start
	for lo > 0 && text[lo-1] != ';' && text[lo-1] != '\n' && text[lo-1] != '\r' {
		lo--
	}
	hi := end
	for hi < len(text) && text[hi] != ';' && text[hi] != '\n' && text[hi] != '\r' {
		hi++
	}
	return lo, hi
}

// hasContext reports whether any keyword appears as a whole word within a
// window around the span [start,end), bounded by the field containing the span.
// It is used to treat a requisite as explicitly signed so it is masked even
// when a checksum fails. The field bound keeps a label in a neighbouring field
// from affecting this value.
func hasContext(text string, start, end int, keywords []string) bool {
	flo, fhi := fieldBounds(text, start, end)
	lo := start - 60
	if lo < flo {
		lo = flo
	}
	hi := end + 60
	if hi > fhi {
		hi = fhi
	}
	lo = snapToRuneStart(text, lo)
	hi = snapToRuneEnd(text, hi)
	window := strings.ToLower(text[lo:hi])
	for _, kw := range keywords {
		if containsWord(window, kw) {
			return true
		}
	}
	return false
}

// containsWord reports whether word occurs in s as a whole word (not as a
// substring of a longer word). s and word must be lowercase.
func containsWord(s, word string) bool {
	for {
		i := strings.Index(s, word)
		if i < 0 {
			return false
		}
		before := i == 0 || !isLetterBefore(s, i)
		after := i+len(word) >= len(s) || !isLetterAfter(s, i+len(word))
		if before && after {
			return true
		}
		s = s[i+len(word):]
	}
}

// isLetterBefore reports whether the rune ending just before off is a letter.
// off must be on a rune boundary. Decoding the rune instead of inspecting a
// single byte keeps the boundary check correct for multi-byte letters and for
// non-letter multi-byte characters such as a non-breaking space.
func isLetterBefore(s string, off int) bool {
	if off <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:off])
	return unicode.IsLetter(r)
}

// isLetterAfter reports whether the rune starting at off is a letter. off must
// be on a rune boundary.
func isLetterAfter(s string, off int) bool {
	if off >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[off:])
	return unicode.IsLetter(r)
}
