package recognizer

// pinKeywords mark a 4-digit number as a PIN.
var pinKeywords = []string{"пин", "пин-код", "пинкод", "pin", "pin-код", "pin code"}

// PINRecognizer recognizes PIN codes. A bare 4-digit number is never a PIN; a
// context word is required.
type PINRecognizer struct{}

// Type implements Recognizer.
func (PINRecognizer) Type() Type { return PIN }

// Find implements Recognizer.
func (PINRecognizer) Find(text string) ([]Fragment, error) {
	return findContextSigned(text, "", 4, pinKeywords, PIN)
}
