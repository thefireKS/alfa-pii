package recognizer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// fullNameLabels mark a capitalized word sequence as a client's full name
// rather than a reference mention of a person. A bare name such as "поэт
// Александр Пушкин" has none of these labels and is not masked.
var fullNameLabels = []string{
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

// fullNameFieldLabels are explicit field labels that directly introduce the
// value that follows them. They support upper, lower and mixed case, so the
// value is matched case-insensitively. "имя" is deliberately excluded here
// because it also begins the card-holder label "имя на карте"; a single name
// after "имя" is handled by singleNameLabels instead.
var fullNameFieldLabels = []string{"фио", "фамилия", "отчество"}

// fullNameRe matches a full name: two or three capitalized Cyrillic words, or
// an initials form such as "И. И. Иванов" or "Иванов И. И.". All-caps
// abbreviations (e.g. "ОУФМС") do not match because the pattern requires a
// lowercase tail after the first letter.
var fullNameRe = regexp.MustCompile(
	`[А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+){1,2}|` +
		`[А-ЯЁ]\.\s*[А-ЯЁ]\.\s+[А-ЯЁ][а-яё]+|` +
		`[А-ЯЁ][а-яё]+\s+[А-ЯЁ]\.\s*[А-ЯЁ]?\.?`)

// fullNameValueRe matches a full name in any case (upper, lower or mixed) for
// an explicitly labeled field. It covers two or three Cyrillic words and the
// initials forms.
var fullNameValueRe = regexp.MustCompile(
	`(?i:[а-яё]{2,}(?:\s+[а-яё]{2,}){1,2})|` +
		`(?i:[а-яё]\.\s*[а-яё]\.\s+[а-яё]+)|` +
		`(?i:[а-яё]+\s+[а-яё]\.\s*[а-яё]?\.?)`)

// singleNameRe matches a single capitalized word used as a surname, given name
// or patronymic after an explicit field label.
var singleNameRe = regexp.MustCompile(`[А-ЯЁ][а-яё]+`)

// singleNameLabels are field labels that introduce a single-word name value.
var singleNameLabels = []string{"фамилия", "отчество", "имя"}

// fullNameNegativeLabels mark a following name as a reference mention of a
// person (for example a poet) rather than the client's own name. They are role
// markers, not a list of excluded names, so a client who genuinely bears the
// name is still masked when the client label introduces it directly.
var fullNameNegativeLabels = []string{
	"поэт", "поэта", "поэту", "поэтом", "поэте",
	"писатель", "писателя", "писателю", "писателем",
	"художник", "художника", "художнику", "художником",
	"композитор", "композитора", "композитору", "композитором",
	"учёный", "ученый", "учёного", "ученого",
	"актёр", "актер", "актёра", "актера",
	"певец", "певца", "режиссёр", "режиссер", "режиссёра", "режиссера",
}

// FullNameRecognizer recognizes a client's full name. The role is set by a
// nearby client label; a reference mention of a person without such a label is
// not masked. Explicit field labels (фио, фамилия, отчество) introduce the
// value directly and support upper, lower and mixed case; "имя" introduces a
// single capitalized name. Only the limited set of forms above is supported;
// general morphology is not claimed.
type FullNameRecognizer struct{}

// Type implements Recognizer.
func (FullNameRecognizer) Type() Type { return FullName }

// Find implements Recognizer.
func (FullNameRecognizer) Find(text string) ([]Fragment, error) {
	var frags []Fragment

	// Explicit field labels introduce the value directly and support any case.
	for _, m := range findLabelMatches(text, fullNameFieldLabels) {
		lo := m.after
		hi := lo + 60
		if hi > len(text) {
			hi = len(text)
		}
		hi = snapToRuneEnd(text, hi)
		if loc := fullNameValueRe.FindStringIndex(text[lo:hi]); loc != nil {
			frags = append(frags, Fragment{Type: FullName, Start: lo + loc[0], End: lo + loc[1]})
		}
	}

	// Loose client labels set the role of a capitalized name in the window.
	for _, m := range fullNameRe.FindAllStringIndex(text, -1) {
		if hasContext(text, m[0], m[1], fullNameLabels) &&
			!hasContext(text, m[0], m[1], fullNameNegativeLabels) {
			start := trimLeadingLabels(text, m[0], m[1])
			frags = append(frags, Fragment{Type: FullName, Start: start, End: m[1]})
		}
	}

	// Single-word surname/patronymic/given name after an explicit field label.
	for _, m := range findLabelMatches(text, singleNameLabels) {
		lo := m.after
		hi := lo + 60
		if hi > len(text) {
			hi = len(text)
		}
		hi = snapToRuneEnd(text, hi)
		if loc := singleNameRe.FindStringIndex(text[lo:hi]); loc != nil {
			frags = append(frags, Fragment{Type: FullName, Start: lo + loc[0], End: lo + loc[1]})
		}
	}
	return frags, nil
}

// trimLeadingLabels advances start past a leading label word in a full-name
// match, so a capitalized label such as "Клиент" in "Клиент Александр Пушкин"
// is not included in the masked name. The label itself is not sensitive; only
// the name that follows it is masked.
func trimLeadingLabels(text string, start, end int) int {
	i := start
	for i < end && text[i] == ' ' {
		i++
	}
	wordEnd := i
	for wordEnd < end {
		r, size := utf8.DecodeRuneInString(text[wordEnd:])
		if !unicode.IsLetter(r) {
			break
		}
		wordEnd += size
	}
	word := strings.ToLower(text[i:wordEnd])
	for _, l := range fullNameLabels {
		if word == l {
			// Skip the label and any following whitespace so the fragment
			// starts at the name itself.
			for wordEnd < end && text[wordEnd] == ' ' {
				wordEnd++
			}
			return wordEnd
		}
	}
	return start
}
