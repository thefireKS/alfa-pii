package recognizer

import "regexp"

// birthPlaceLabels set the role of a following place as the client's place of
// birth.
var birthPlaceLabels = []string{"место рождения", "родился", "родилась"}

// birthPlaceRe matches a place designation: a place marker (г., город, пос.,
// село, деревня, дер.) followed by a capitalized name, or a capitalized name
// preceded by the preposition "в"/"во". Requiring a marker or preposition
// avoids treating a person's name after "родился" as a place.
var birthPlaceRe = regexp.MustCompile(
	`(?:г\.|город|пос\.|село|деревня|дер\.)\s+[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+)?|` +
		`(?:в|во)\s+[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+)?`)

// BirthPlaceRecognizer recognizes the client's place of birth. The label sets
// the role; a bare place name is not masked.
type BirthPlaceRecognizer struct{}

// Type implements Recognizer.
func (BirthPlaceRecognizer) Type() Type { return BirthPlace }

// Find implements Recognizer.
func (BirthPlaceRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, birthPlaceLabels, birthPlaceRe, BirthPlace, 60)
}
