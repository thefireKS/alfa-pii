package recognizer

// cvvKeywords mark a 3-digit number as a CVV.
var cvvKeywords = []string{"cvv", "код безопасности", "security code", "код с обратной стороны", "код на обратной стороне"}

// CVVRecognizer recognizes card security codes. A bare 3-digit number is never
// a CVV; a context word is required.
type CVVRecognizer struct{}

// Type implements Recognizer.
func (CVVRecognizer) Type() Type { return CVV }

// Find implements Recognizer.
func (CVVRecognizer) Find(text string) ([]Fragment, error) {
	return findContextSigned(text, "", 3, cvvKeywords, CVV)
}
