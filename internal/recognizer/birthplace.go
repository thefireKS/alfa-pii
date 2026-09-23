package recognizer

import "regexp"

// birthPlaceLabels set the role of a following place as the client's place of
// birth.
var birthPlaceLabels = []string{"место рождения", "родился", "родилась"}

// birthPlaceFieldLabels are the labels that directly introduce a place value,
// so a bare capitalized place name is accepted after them. After "родился" a
// bare name could be a person, so a marker or preposition is required there.
var birthPlaceFieldLabels = []string{"место рождения"}

// birthPlaceRe matches a place designation: a place marker (г., город, пос.,
// село, деревня, дер.) followed by a capitalized name, or a capitalized name
// preceded by the preposition "в"/"во". Hyphenated names such as
// "Санкт-Петербург" and "Ростове-на-Дону" are supported; hyphenated parts may
// be lowercase (на, в) or capitalized (Петербург). Requiring a marker or
// preposition avoids treating a person's name after "родился" as a place.
var birthPlaceRe = regexp.MustCompile(
	`(?:г\.|город|пос\.|село|деревня|дер\.)\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*(?:\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*)?|` +
		`(?:в|во)\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*(?:\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*)?`)

// birthPlaceBareRe matches a bare capitalized place name. It is applied only
// after the explicit field label "место рождения", where a capitalized name is
// the place value rather than a person.
var birthPlaceBareRe = regexp.MustCompile(
	`[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*(?:\s+[А-ЯЁ][а-яё]+(?:-[А-ЯЁа-яё]+)*)?`)

// BirthPlaceRecognizer recognizes the client's place of birth. The label sets
// the role; a bare place name is not masked.
type BirthPlaceRecognizer struct{}

// Type implements Recognizer.
func (BirthPlaceRecognizer) Type() Type { return BirthPlace }

// Find implements Recognizer.
func (BirthPlaceRecognizer) Find(text string) ([]Fragment, error) {
	var frags []Fragment
	// Marker or preposition form after any birth-place label.
	fs, err := findLabeledValue(text, birthPlaceLabels, birthPlaceRe, BirthPlace, 60)
	if err != nil {
		return nil, err
	}
	frags = append(frags, fs...)
	// Bare capitalized place name after the explicit field label.
	fs, err = findLabeledValue(text, birthPlaceFieldLabels, birthPlaceBareRe, BirthPlace, 60)
	if err != nil {
		return nil, err
	}
	frags = append(frags, fs...)
	// A bare name nested inside a marker form (e.g. "Москва" inside
	// "г. Москва") is redundant; keep only the outer marker form.
	return dedupeNested(frags), nil
}

// dedupeNested removes fragments that are nested inside another fragment of the
// same type, keeping the outer range. It is used when a recognizer matches both
// a composite form and a bare part of it.
func dedupeNested(frags []Fragment) []Fragment {
	out := make([]Fragment, 0, len(frags))
	for _, f := range frags {
		nested := false
		for _, o := range frags {
			if o.Type == f.Type && o.Start <= f.Start && o.End >= f.End && (o.Start != f.Start || o.End != f.End) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, f)
		}
	}
	return out
}
