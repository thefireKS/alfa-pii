package recognizer

// citizenshipLabels set the role of a following country as the client's
// citizenship.
var citizenshipLabels = []string{"гражданство", "гражданин", "гражданка", "гражданина"}

// citizenshipValues is a fixed dictionary of country names and their common
// genitive forms. A dictionary is used instead of a generic capitalized-word
// pattern so a person's name after "гражданин" is not mistaken for a
// citizenship value. Only the listed case variants are supported.
var citizenshipValues = []string{
	"Российская Федерация", "РФ", "Россия", "России",
	"Казахстан", "Казахстана", "Беларусь", "Беларуси", "Белоруссия", "Белоруссии",
	"Украина", "Украины", "Узбекистан", "Узбекистана",
	"Таджикистан", "Таджикистана", "Киргизия", "Киргизии", "Кыргызстан", "Кыргызстана",
	"Армения", "Армении", "Азербайджан", "Азербайджана",
	"Грузия", "Грузии", "Молдова", "Молдовы", "Молдавия", "Молдавии",
	"Латвия", "Латвии", "Литва", "Литвы", "Эстония", "Эстонии",
	"Германия", "Германии", "Франция", "Франции", "Италия", "Италии",
	"Испания", "Испании", "США", "Китай", "Китая",
	"Турция", "Турции", "Израиль", "Израиля", "Финляндия", "Финляндии",
	"Польша", "Польши", "Чехия", "Чехии", "Великобритания", "Великобритании",
}

// CitizenshipRecognizer recognizes the client's citizenship. The value must be
// a known country name near a citizenship label; a bare country name is not
// masked.
type CitizenshipRecognizer struct{}

// Type implements Recognizer.
func (CitizenshipRecognizer) Type() Type { return Citizenship }

// Find implements Recognizer.
func (CitizenshipRecognizer) Find(text string) ([]Fragment, error) {
	var frags []Fragment
	for _, m := range findDictRanges(text, citizenshipValues) {
		if hasContext(text, m[0], m[1], citizenshipLabels) {
			frags = append(frags, Fragment{Type: Citizenship, Start: m[0], End: m[1]})
		}
	}
	return frags, nil
}
