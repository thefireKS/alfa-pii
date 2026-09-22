package recognizer

// cardKeywords mark a number as a payment card when the Luhn check fails.
var cardKeywords = []string{"карта", "карты", "карту", "картой", "card", "номер карты", "платёжная карта", "банковская карта"}

// CardRecognizer recognizes payment card numbers.
type CardRecognizer struct{}

// Type implements Recognizer.
func (CardRecognizer) Type() Type { return Card }

// Find implements Recognizer. A card is 13-19 digits passing the Luhn check;
// an explicitly signed card is masked even when Luhn fails.
func (CardRecognizer) Find(text string) ([]Fragment, error) {
	spans := scanSpans(text, "- ")
	var frags []Fragment
	for _, sp := range spans {
		d := sp.digits
		if len(d) < 13 || len(d) > 19 {
			continue
		}
		if !luhn(d) && !hasContext(text, sp.start, sp.end, cardKeywords) {
			continue
		}
		if hasNegativeContext(text, sp.start, sp.end) {
			continue
		}
		frags = append(frags, Fragment{Type: Card, Start: sp.start, End: sp.end})
	}
	return frags, nil
}

func luhn(d string) bool {
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		v := int(d[i] - '0')
		if double {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return sum%10 == 0
}
