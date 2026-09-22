package recognizer

import "regexp"

// fullNameLabels mark a capitalized word sequence as a client's full name
// rather than a reference mention of a person. A bare name such as "поэт
// Александр Пушкин" has none of these labels and is not masked.
var fullNameLabels = []string{
	"фио", "фамилия", "имя", "отчество",
	"клиент", "клиента", "заявитель", "заявителя",
	"владелец", "владельца", "держатель", "держателя",
	"гражданин", "гражданина", "гражданка",
	"родился", "родилась",
	"проживает", "проживающий", "проживающая",
	"зарегистрирован", "зарегистрирована",
	"паспорт", "паспорта",
	"выдан", "выдано",
	"держатель карты", "card holder", "имя на карте", "name on card",
}

// fullNameRe matches a full name: two or three capitalized Cyrillic words, or
// an initials form such as "И. И. Иванов" or "Иванов И. И.". All-caps
// abbreviations (e.g. "ОУФМС") do not match because the pattern requires a
// lowercase tail after the first letter.
var fullNameRe = regexp.MustCompile(
	`[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+){1,2}|` +
		`[А-ЯЁ]\.\s*[А-ЯЁ]\.\s+[А-ЯЁ][а-яё]+|` +
		`[А-ЯЁ][а-яё]+\s+[А-ЯЁ]\.\s*[А-ЯЁ]?\.?`)

// singleNameRe matches a single capitalized word used as a surname or
// patronymic after an explicit field label.
var singleNameRe = regexp.MustCompile(`[А-ЯЁ][а-яё]+`)

// singleNameLabels are field labels that introduce a single-word name value.
var singleNameLabels = []string{"фамилия", "отчество"}

// FullNameRecognizer recognizes a client's full name. The role is set by a
// nearby client label; a reference mention of a person without such a label is
// not masked. Only the limited set of forms above is supported; general
// morphology is not claimed.
type FullNameRecognizer struct{}

// Type implements Recognizer.
func (FullNameRecognizer) Type() Type { return FullName }

// Find implements Recognizer.
func (FullNameRecognizer) Find(text string) ([]Fragment, error) {
	var frags []Fragment
	for _, m := range fullNameRe.FindAllStringIndex(text, -1) {
		if hasContext(text, m[0], m[1], fullNameLabels) {
			frags = append(frags, Fragment{Type: FullName, Start: m[0], End: m[1]})
		}
	}
	for _, m := range singleNameRe.FindAllStringIndex(text, -1) {
		if hasContext(text, m[0], m[1], singleNameLabels) {
			frags = append(frags, Fragment{Type: FullName, Start: m[0], End: m[1]})
		}
	}
	return frags, nil
}
