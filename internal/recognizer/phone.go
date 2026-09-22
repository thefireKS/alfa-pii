package recognizer

import "strings"

// phoneKeywords mark a number as a phone when it lacks an explicit +7 or 8
// prefix.
var phoneKeywords = []string{"телефон", "тел", "мобильный", "сотовый", "phone", "tel", "mobile"}

// PhoneRecognizer recognizes Russian phone numbers.
type PhoneRecognizer struct{}

// Type implements Recognizer.
func (PhoneRecognizer) Type() Type { return Phone }

// Find implements Recognizer. A phone is 11 digits starting with 7 or 8. The
// +7 and 8-prefixed forms are unambiguous; a bare 7-prefixed number requires a
// context word so order numbers are not masked.
func (PhoneRecognizer) Find(text string) ([]Fragment, error) {
	spans := scanSpans(text, "+()- ")
	var frags []Fragment
	for _, sp := range spans {
		d := sp.digits
		if len(d) != 11 || (d[0] != '7' && d[0] != '8') {
			continue
		}
		start := sp.start
		// Include a leading '+' in the span.
		if start > 0 && text[start-1] == '+' {
			start--
		}
		raw := text[start:sp.end]
		hasPlus := strings.HasPrefix(raw, "+")
		if hasPlus && d[0] != '7' {
			continue
		}
		if !hasPlus && d[0] != '8' && !hasContext(text, start, sp.end, phoneKeywords) {
			continue
		}
		if hasNegativeContext(text, start, sp.end) {
			continue
		}
		frags = append(frags, Fragment{Type: Phone, Start: start, End: sp.end})
	}
	return frags, nil
}
