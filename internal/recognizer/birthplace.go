package recognizer

import "regexp"

// birthPlaceLabels set the role of a following place as the client's place of
// birth.
var birthPlaceLabels = []string{"место рождения", "родился", "родилась"}

// birthPlaceRe matches a place designation: a place marker (г., город, пос.,
// село, деревня, дер.) followed by a capitalized name, or a capitalized name
// preceded by the preposition "в"/"во". Hyphenated names such as
// "Санкт-Петербург" and "Ростове-на-Дону" are supported; hyphenated parts may
// be lowercase (на, в) or capitalized (Петербург). Requiring a marker or
// preposition avoids treating a person's name after "родился" as a place.
var birthPlaceRe = regexp.MustCompile(
	`(?:г\.|город|пос\.|село|деревня|дер\.)\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*(?:\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*)?|` +
		`(?:в|во)\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*(?:\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*)?`)

// BirthPlaceRecognizer recognizes the client's place of birth. The label sets
// the role; a bare place name is not masked.
type BirthPlaceRecognizer struct{}

// Type implements Recognizer.
func (BirthPlaceRecognizer) Type() Type { return BirthPlace }

// Find implements Recognizer.
func (BirthPlaceRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, birthPlaceLabels, birthPlaceRe, BirthPlace, 60)
}
