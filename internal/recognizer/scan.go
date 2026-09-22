package recognizer

import "strings"

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

// hasContext reports whether any keyword appears as a whole word within a
// window around the span [start,end). It is used to treat a requisite as
// explicitly signed so it is masked even when a checksum fails.
func hasContext(text string, start, end int, keywords []string) bool {
	lo := start - 60
	if lo < 0 {
		lo = 0
	}
	hi := end + 60
	if hi > len(text) {
		hi = len(text)
	}
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
		before := i == 0 || !isLetter(s[i-1])
		after := i+len(word) >= len(s) || !isLetter(s[i+len(word)])
		if before && after {
			return true
		}
		s = s[i+len(word):]
	}
}

// isLetter reports whether b is a Latin or Cyrillic letter byte. Bytes >= 0x80
// are treated as letters to cover Cyrillic multi-byte sequences.
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b >= 0x80
}
