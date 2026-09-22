package recognizer

import "regexp"

// cardHolderLabels set the role of a following name as the card holder's name.
var cardHolderLabels = []string{
	"держатель карты", "card holder", "cardholder",
	"имя на карте", "name on card", "имя держателя",
}

// cardHolderRe matches a card holder name in Latin or Cyrillic: one to three
// capitalized words. Latin is supported because card holder names are often
// embossed in Latin.
var cardHolderRe = regexp.MustCompile(`[A-Za-zА-ЯЁ][A-Za-zА-ЯЁа-яё]+(?:\s+[A-Za-zА-ЯЁ][A-Za-zА-ЯЁа-яё]+){0,2}`)

// CardHolderNameRecognizer recognizes the name of the payment card holder. The
// label sets the role; a bare capitalized name is not masked.
type CardHolderNameRecognizer struct{}

// Type implements Recognizer.
func (CardHolderNameRecognizer) Type() Type { return CardHolderName }

// Find implements Recognizer.
func (CardHolderNameRecognizer) Find(text string) ([]Fragment, error) {
	return findLabeledValue(text, cardHolderLabels, cardHolderRe, CardHolderName, 60)
}
