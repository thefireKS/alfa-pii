package recognizer

import "regexp"

// dateRe matches a date in numeric (DD.MM.YYYY) or word (12 марта 1990 года)
// form. The month is matched case-insensitively so "12 МАРТА 1990 года" is
// recognized too. A bare date carries no role; the label that precedes it
// decides the category.
var dateRe = regexp.MustCompile(
	`\d{1,2}[./-]\d{1,2}[./-]\d{2,4}|` +
		`\d{1,2}\s+(?i:января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)\s+\d{4}(?:\s+года?)?`)

// birthDateLabels set the role of a following date as the client's date of
// birth.
var birthDateLabels = []string{"дата рождения", "родился", "родилась"}

// BirthDateRecognizer recognizes the client's date of birth. The label "дата
// рождения" (or "родился"/"родилась") sets the role; a bare date is not
// masked.
type BirthDateRecognizer struct{}

// Type implements Recognizer.
func (BirthDateRecognizer) Type() Type { return BirthDate }

// Find implements Recognizer.
func (BirthDateRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, birthDateLabels, dateRe, BirthDate, 60)
}
