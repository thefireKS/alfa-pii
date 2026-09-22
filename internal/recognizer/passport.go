package recognizer

// passportKeywords mark a 4+6 digit sequence as a passport series and number.
var passportKeywords = []string{"паспорт", "паспорта", "паспортом", "серия", "номер паспорта"}

// PassportRecognizer recognizes Russian passport series and number (4+6
// digits).
type PassportRecognizer struct{}

// Type implements Recognizer.
func (PassportRecognizer) Type() Type { return Passport }

// Find implements Recognizer. The 10-digit form is ambiguous on its own, so a
// context word is required to avoid masking order numbers.
func (PassportRecognizer) Find(text string) ([]Fragment, error) {
	return findContextSigned(text, "- ", 10, passportKeywords, Passport)
}
