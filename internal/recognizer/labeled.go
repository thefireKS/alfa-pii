package recognizer

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// labelMatch records the byte offset just after a whole-word occurrence of a
// role-setting label. The label itself is not sensitive; only the value that
// follows it is masked.
type labelMatch struct {
	after int
}

// lowerWithMap returns the lowercased text and a mapping from each byte offset
// in the lowercased text to the corresponding byte offset in the original text.
// strings.ToLower can change the byte length of a rune (for example U+212A
// KELVIN SIGN becomes 'k'), so offsets found in the lowercased text must be
// translated back to the original text before they are used to slice it. The
// mapping has one extra entry for the end of the string.
func lowerWithMap(text string) (string, []int) {
	lower := make([]byte, 0, len(text))
	mapping := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		lr := unicode.ToLower(r)
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], lr)
		for _, b := range buf[:n] {
			lower = append(lower, b)
			mapping = append(mapping, i)
		}
		i += size
	}
	mapping = append(mapping, len(text))
	return string(lower), mapping
}

// findLabelMatches returns the offsets after each whole-word occurrence of any
// label in text. Labels are matched case-insensitively. The returned offsets
// are byte offsets into the original text.
func findLabelMatches(text string, labels []string) []labelMatch {
	lower, mapping := lowerWithMap(text)
	var matches []labelMatch
	for _, label := range labels {
		ll := strings.ToLower(label)
		for i := 0; ; {
			j := strings.Index(lower[i:], ll)
			if j < 0 {
				break
			}
			pos := i + j
			if isWordBoundary(lower, pos, len(ll)) {
				after := pos + len(ll)
				matches = append(matches, labelMatch{after: mapping[after]})
			}
			i = pos + len(ll)
		}
	}
	return matches
}

// isWordBoundary reports whether the label at [pos, pos+length) in s is a whole
// word (not part of a longer word). s must be lowercase. The boundary is
// checked on decoded runes so a multi-byte letter is not mistaken for a
// non-letter and a non-letter multi-byte character (such as a non-breaking
// space) is not mistaken for a letter.
func isWordBoundary(s string, pos, length int) bool {
	before := pos == 0 || !isLetterBefore(s, pos)
	after := pos+length >= len(s) || !isLetterAfter(s, pos+length)
	return before && after
}

// findLabeledValue returns fragments for the first value matching valueRe that
// follows each label within a window. The label sets the role of the fragment;
// only the value is masked, keeping the fragment minimal. A label with no
// matching value in the window contributes nothing.
func findLabeledValue(text string, labels []string, valueRe *regexp.Regexp, typ Type, window int) ([]Fragment, error) {
	var frags []Fragment
	for _, m := range findLabelMatches(text, labels) {
		lo := m.after
		hi := lo + window
		if hi > len(text) {
			hi = len(text)
		}
		hi = snapToRuneEnd(text, hi)
		loc := valueRe.FindStringIndex(text[lo:hi])
		if loc == nil {
			continue
		}
		frags = append(frags, Fragment{Type: typ, Start: lo + loc[0], End: lo + loc[1]})
	}
	return frags, nil
}

// findDictRanges returns byte ranges of every whole-word occurrence of any
// value in the dictionary. Values are matched case-insensitively. The returned
// ranges are byte offsets into the original text.
func findDictRanges(text string, values []string) [][2]int {
	lower, mapping := lowerWithMap(text)
	var out [][2]int
	for _, v := range values {
		lv := strings.ToLower(v)
		for i := 0; ; {
			j := strings.Index(lower[i:], lv)
			if j < 0 {
				break
			}
			pos := i + j
			if isWordBoundary(lower, pos, len(lv)) {
				out = append(out, [2]int{mapping[pos], mapping[pos+len(lv)]})
			}
			i = pos + len(lv)
		}
	}
	return out
}

// lastWordIndex returns the byte offset of the last whole-word occurrence of
// word in s, or -1 if absent. s and word must be lowercase.
func lastWordIndex(s, word string) int {
	last := -1
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			break
		}
		pos := i + j
		if isWordBoundary(s, pos, len(word)) {
			last = pos
		}
		i = pos + len(word)
	}
	return last
}
