package recognizer

import (
	"regexp"
	"strings"
)

// addressPartRe matches a single address part: a city, street, building,
// apartment or region designation. An address is a sequence of such parts.
var addressPartRe = regexp.MustCompile(
	`(?:г\.|город)\s+[А-ЯЁ][а-яё]+|` +
		`(?:ул\.|улица)\s+[А-ЯЁ][а-яё]+|` +
		`(?:проспект|пр-т)\s+[А-ЯЁ][а-яё]+|` +
		`(?:д\.|дом)\s+\d+|` +
		`корп\.\s*\d+|` +
		`(?:кв\.|квартира)\s+\d+|` +
		`[А-ЯЁ][а-яё]+(?:ская|ский|ское)?\s+область|` +
		`[А-ЯЁ][а-яё]+(?:ская|ский|ское)?\s+край|` +
		`[А-ЯЁ][а-яё]+(?:ская|ский|ское)?\s+республика`)

// clientAddressLabels mark an address as the client's place of residence.
var clientAddressLabels = []string{
	"адрес клиента", "адрес проживания", "адрес регистрации",
	"место жительства", "место регистрации",
	"проживает", "проживающий", "проживающая",
	"зарегистрирован", "зарегистрирована",
	"прописан", "прописана",
}

// branchAddressLabels mark an address as bank-branch requisites rather than
// client data. The exclusion is per-fragment: only an address whose nearest
// owner label is a branch label is skipped.
var branchAddressLabels = []string{
	"адрес отделения", "адрес банка", "адрес офиса", "адрес филиала",
	"отделение", "банк", "офис", "филиал",
}

// AddressRecognizer recognizes a client's address from its composite parts and
// the nearest owner label. An address owned by a bank branch is not masked as
// client data.
type AddressRecognizer struct{}

// Type implements Recognizer.
func (AddressRecognizer) Type() Type { return Address }

// Find implements Recognizer.
func (AddressRecognizer) Find(text string) ([]Fragment, error) {
	var frags []Fragment
	for _, g := range findAddressGroups(text) {
		client, ok := addressOwner(text, g[0])
		if !ok || !client {
			continue
		}
		frags = append(frags, Fragment{Type: Address, Start: g[0], End: g[1]})
	}
	return frags, nil
}

// findAddressGroups returns byte ranges of maximal sequences of consecutive
// address parts, where consecutive parts are separated only by commas or
// whitespace.
func findAddressGroups(text string) [][2]int {
	idx := addressPartRe.FindAllStringIndex(text, -1)
	var groups [][2]int
	for _, m := range idx {
		if len(groups) > 0 && isAddressGap(text, groups[len(groups)-1][1], m[0]) {
			groups[len(groups)-1][1] = m[1]
		} else {
			groups = append(groups, [2]int{m[0], m[1]})
		}
	}
	return groups
}

// isAddressGap reports whether the bytes between two address parts are only
// commas or whitespace, so the parts belong to one address.
func isAddressGap(text string, from, to int) bool {
	for i := from; i < to; i++ {
		switch text[i] {
		case ',', ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// addressOwner reports whether the address starting at start is owned by a
// client (true) or by a bank branch (false), based on the nearest role label
// in the window before it. ok is false when no role label is found, in which
// case the address is not masked.
func addressOwner(text string, start int) (client bool, ok bool) {
	lo := start - 80
	if lo < 0 {
		lo = 0
	}
	lower := strings.ToLower(text[lo:start])
	bestPos := -1
	bestClient := false
	for _, l := range clientAddressLabels {
		if p := lastWordIndex(lower, strings.ToLower(l)); p > bestPos {
			bestPos = p
			bestClient = true
		}
	}
	for _, l := range branchAddressLabels {
		if p := lastWordIndex(lower, strings.ToLower(l)); p > bestPos {
			bestPos = p
			bestClient = false
		}
	}
	if bestPos < 0 {
		return false, false
	}
	return bestClient, true
}
