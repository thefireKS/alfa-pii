package recognizer

// innKeywords mark a number as an INN when the checksum fails.
var innKeywords = []string{"инн", "inn", "идентификационный номер налогоплательщика"}

// INNRecognizer recognizes Russian tax numbers (10 or 12 digits).
type INNRecognizer struct{}

// Type implements Recognizer.
func (INNRecognizer) Type() Type { return INN }

// Find implements Recognizer. A valid checksum is a strong signal; an
// explicitly signed INN is masked even when the checksum fails.
func (INNRecognizer) Find(text string) ([]Fragment, error) {
	spans := scanSpans(text, "- ")
	var frags []Fragment
	for _, sp := range spans {
		d := sp.digits
		if len(d) != 10 && len(d) != 12 {
			continue
		}
		if !validINN(d) && !hasContext(text, sp.start, sp.end, innKeywords) {
			continue
		}
		if hasNegativeContext(text, sp.start, sp.end) {
			continue
		}
		frags = append(frags, Fragment{Type: INN, Start: sp.start, End: sp.end})
	}
	return frags, nil
}

func validINN(d string) bool {
	if len(d) == 10 {
		return innCheck(d, []int{2, 4, 10, 3, 5, 9, 4, 6, 8}) == int(d[9]-'0')
	}
	if len(d) == 12 {
		if innCheck(d, []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) != int(d[10]-'0') {
			return false
		}
		return innCheck(d, []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}) == int(d[11]-'0')
	}
	return false
}

func innCheck(d string, weights []int) int {
	sum := 0
	for i, w := range weights {
		sum += int(d[i]-'0') * w
	}
	return sum % 11 % 10
}
