package recognizer

import "regexp"

// authorityLabels set the role of a following capitalized sequence as the
// passport issuing authority.
var authorityLabels = []string{"кем выдан", "орган выдачи", "выдан", "выдано"}

// authorityRe matches an issuing-authority name: a sequence of capitalized
// words and abbreviations with a few lowercase connectors (по, г., р-н,
// области, ...). It stops at a comma, period or other non-matching character.
var authorityRe = regexp.MustCompile(
	`[А-ЯЁ][А-ЯЁа-яё0-9]*(?:\s+(?:[А-ЯЁ][А-ЯЁа-яё0-9]*|по|г\.|р-н|район|области|края|республики|отдела|отделом|управления|управлением|города|москвы|санкт-петербурга)){1,8}`)

// PassportAuthorityRecognizer recognizes the passport issuing authority. The
// label sets the role; a bare capitalized sequence is not masked.
type PassportAuthorityRecognizer struct{}

// Type implements Recognizer.
func (PassportAuthorityRecognizer) Type() Type { return PassportAuthority }

// Find implements Recognizer.
func (PassportAuthorityRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, authorityLabels, authorityRe, PassportAuthority, 60)
}
