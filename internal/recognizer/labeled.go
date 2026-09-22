package recognizer

import (
	"regexp"
	"strings"
)

// labelMatch records the byte offset just after a whole-word occurrence of a
// role-setting label. The label itself is not sensitive; only the value that
// follows it is masked.
type labelMatch struct {
	after int
}

// findLabelMatches returns the offsets after each whole-word occurrence of any
// label in text. Labels are matched case-insensitively.
func findLabelMatches(text string, labels []string) []labelMatch {
	lower := strings.ToLower(text)
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
				matches = append(matches, labelMatch{after: pos + len(ll)})
			}
			i = pos + len(ll)
		}
	}
	return matches
}

// isWordBoundary reports whether the label at [pos, pos+length) in s is a whole
// word (not part of a longer word). s must be lowercase.
func isWordBoundary(s string, pos, length int) bool {
	before := pos == 0 || !isLetter(s[pos-1])
	after := pos+length >= len(s) || !isLetter(s[pos+length])
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
		loc := valueRe.FindStringIndex(text[lo:hi])
		if loc == nil {
			continue
		}
		frags = append(frags, Fragment{Type: typ, Start: lo + loc[0], End: lo + loc[1]})
	}
	return frags, nil
}

// findDictRanges returns byte ranges of every whole-word occurrence of any
// value in the dictionary. Values are matched case-insensitively.
func findDictRanges(text string, values []string) [][2]int {
	lower := strings.ToLower(text)
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
				out = append(out, [2]int{pos, pos + len(v)})
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
