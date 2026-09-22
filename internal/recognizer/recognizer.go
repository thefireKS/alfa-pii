// Package recognizer finds sensitive fragments in text. Each recognizer
// reports the data type and the byte range (UTF-8) of every fragment it
// finds. Byte offsets are the single unit of measure shared by all
// recognizers and the replacement mechanism.
package recognizer

import "regexp"

// Type identifies a category of sensitive data.
type Type string

// Supported data types. Only email is implemented in this milestone; other
// categories are added by later tasks and are intentionally not listed here.
const (
	Email Type = "email"
)

// Fragment is a single sensitive occurrence in a text.
type Fragment struct {
	Type Type
	// Start and End are byte offsets into the original UTF-8 text. End is
	// exclusive.
	Start int
	End   int
}

// Recognizer finds fragments of a data type in a text.
type Recognizer interface {
	// Type returns the data type this recognizer detects.
	Type() Type
	// Find returns all fragments of the type found in text.
	Find(text string) []Fragment
}

// emailRe is a general email rule: local part, '@', domain with at least one
// dot. It is intentionally permissive; precision is refined by later tasks.
var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// EmailRecognizer recognizes email addresses.
type EmailRecognizer struct{}

// Type implements Recognizer.
func (EmailRecognizer) Type() Type { return Email }

// Find implements Recognizer. It returns byte ranges of email matches.
func (EmailRecognizer) Find(text string) []Fragment {
	idx := emailRe.FindAllStringIndex(text, -1)
	frags := make([]Fragment, 0, len(idx))
	for _, m := range idx {
		frags = append(frags, Fragment{Type: Email, Start: m[0], End: m[1]})
	}
	return frags
}
