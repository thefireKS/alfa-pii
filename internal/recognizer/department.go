package recognizer

import "strings"

// departmentKeywords mark a XXX-XXX sequence as a department code.
var departmentKeywords = []string{"код подразделения"}

// DepartmentCodeRecognizer recognizes the passport department code (XXX-XXX).
type DepartmentCodeRecognizer struct{}

// Type implements Recognizer.
func (DepartmentCodeRecognizer) Type() Type { return DepartmentCode }

// Find implements Recognizer. The 6-digit form is short and ambiguous, so a
// context word and the mandatory hyphen are required.
func (DepartmentCodeRecognizer) Find(text string) ([]Fragment, error) {
	spans := scanSpans(text, "- ")
	var frags []Fragment
	for _, sp := range spans {
		if len(sp.digits) != 6 {
			continue
		}
		if !strings.Contains(text[sp.start:sp.end], "-") {
			continue
		}
		if !hasContext(text, sp.start, sp.end, departmentKeywords) {
			continue
		}
		if hasNegativeContext(text, sp.start, sp.end) {
			continue
		}
		frags = append(frags, Fragment{Type: DepartmentCode, Start: sp.start, End: sp.end})
	}
	return frags, nil
}
