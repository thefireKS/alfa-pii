package recognizer

import "regexp"

// emailRe is a general email rule: local part, '@', domain with at least one
// dot. It is compiled once at package initialization.
var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// EmailRecognizer recognizes email addresses.
type EmailRecognizer struct{}

// Type implements Recognizer.
func (EmailRecognizer) Type() Type { return Email }

// Find implements Recognizer.
func (EmailRecognizer) Find(text string) ([]Fragment, error) {
	idx := emailRe.FindAllStringIndex(text, -1)
	frags := make([]Fragment, 0, len(idx))
	for _, m := range idx {
		frags = append(frags, Fragment{Type: Email, Start: m[0], End: m[1]})
	}
	return frags, nil
}
