package recognizer

// issueDateLabels set the role of a following date as the passport issue date.
// The label "дата выдачи" is the primary signal; "выдан"/"выдано" followed by
// a date also set the role. A bare date is not masked.
var issueDateLabels = []string{"дата выдачи", "выдан", "выдано"}

// PassportIssueDateRecognizer recognizes the passport issue date. It is a
// distinct category from the date of birth: the label decides which one a date
// belongs to.
type PassportIssueDateRecognizer struct{}

// Type implements Recognizer.
func (PassportIssueDateRecognizer) Type() Type { return PassportIssueDate }

// Find implements Recognizer.
func (PassportIssueDateRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, issueDateLabels, dateRe, PassportIssueDate, 60)
}
