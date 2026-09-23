// Package masker replaces sensitive fragments with markers and restores the
// original text from a replacement table. Markers are chosen so they never
// collide with text that already exists in the input.
package masker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Range is a byte range [Start, End) into the original UTF-8 text.
type Range struct {
	Start int
	End   int
}

// Replacement maps a marker to the original value it stands for.
type Replacement struct {
	Marker   string
	Original string
}

// Masker turns recognized fragments into markers.
type Masker struct {
	prefix string
}

// New returns a Masker that generates markers with the given prefix.
func New(prefix string) *Masker {
	return &Masker{prefix: prefix}
}

// Mask replaces the given ranges in text with markers and returns the masked
// text together with the replacement table. Overlapping or adjacent ranges
// are merged so each byte is replaced at most once. Generated markers never
// collide with text already present in the input.
func (m *Masker) Mask(text string, ranges []Range) (string, []Replacement) {
	if len(ranges) == 0 {
		return text, nil
	}
	merged := mergeRanges(ranges)
	// Collect the marker numbers already present in the input once, so marker
	// allocation never collides with existing content and never reuses a marker
	// already allocated in this operation. Scanning the text once keeps the cost
	// bounded even when the text contains many existing markers.
	used := m.usedMarkers(text)
	next := 0
	table := make([]Replacement, 0, len(merged))
	var b strings.Builder
	last := 0
	for _, r := range merged {
		b.WriteString(text[last:r.Start])
		marker, n := m.nextMarker(used, next)
		next = n + 1
		b.WriteString(marker)
		table = append(table, Replacement{Marker: marker, Original: text[r.Start:r.End]})
		last = r.End
	}
	b.WriteString(text[last:])
	return b.String(), table
}

// Stars replaces the given ranges in text with a fixed run of asterisks and
// returns the masked text together with a table mapping each star run to its
// original. Star runs are ambiguous: two fragments produce identical runs, so
// substitution-based restoration is not meaningful. Callers must restrict this
// format to exact restoration of the returned mask.
func Stars(text string, ranges []Range) (string, []Replacement) {
	if len(ranges) == 0 {
		return text, nil
	}
	merged := mergeRanges(ranges)
	table := make([]Replacement, 0, len(merged))
	var b strings.Builder
	last := 0
	for _, r := range merged {
		b.WriteString(text[last:r.Start])
		b.WriteString(starRun)
		table = append(table, Replacement{Marker: starRun, Original: text[r.Start:r.End]})
		last = r.End
	}
	b.WriteString(text[last:])
	return b.String(), table
}

// starRun is the fixed replacement used by the stars format.
const starRun = "****"

// Restore replaces every marker in masked with its original value from the
// table. Markers not present in the table are left untouched. Substitution is a
// single left-to-right pass: an original value inserted by an earlier marker is
// never re-scanned, so a value that itself looks like a marker is not
// substituted again.
func Restore(masked string, table []Replacement) string {
	if len(table) == 0 {
		return masked
	}
	// Map each marker to its original. Match the longest marker first so a
	// marker that is a prefix of another does not shadow it.
	byMarker := make(map[string]string, len(table))
	markers := make([]string, 0, len(table))
	for _, r := range table {
		if _, ok := byMarker[r.Marker]; !ok {
			byMarker[r.Marker] = r.Original
			markers = append(markers, r.Marker)
		}
	}
	sort.Slice(markers, func(i, j int) bool {
		return len(markers[i]) > len(markers[j])
	})
	var b strings.Builder
	b.Grow(len(masked))
	for i := 0; i < len(masked); {
		// Markers start with '[', so skip the marker loop for other bytes.
		if masked[i] == '[' {
			matched := false
			for _, mk := range markers {
				if strings.HasPrefix(masked[i:], mk) {
					b.WriteString(byMarker[mk])
					i += len(mk)
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		b.WriteByte(masked[i])
		i++
	}
	return b.String()
}

// nextMarker returns a marker of the form prefix_N that is not in used, so it
// cannot collide with existing content or with a marker already allocated in
// this operation. The chosen number is recorded in used and returned so the
// caller can continue allocation past it. start is the lowest number to try.
func (m *Masker) nextMarker(used map[int]struct{}, start int) (string, int) {
	n := start
	for {
		if _, ok := used[n]; !ok {
			used[n] = struct{}{}
			return fmt.Sprintf("[%s_%d]", m.prefix, n), n
		}
		n++
	}
}

// usedMarkers returns the set of marker numbers of the form prefix_N that occur
// in text. It scans the text once, so the cost is linear in the text length
// regardless of how many markers are present.
func (m *Masker) usedMarkers(text string) map[int]struct{} {
	used := make(map[int]struct{})
	open := "[" + m.prefix + "_"
	idx := 0
	for {
		i := strings.Index(text[idx:], open)
		if i < 0 {
			return used
		}
		i += idx
		j := i + len(open)
		digits := j
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j > digits && j < len(text) && text[j] == ']' {
			if n, err := strconv.Atoi(text[digits:j]); err == nil {
				used[n] = struct{}{}
			}
		}
		idx = j
	}
}

// mergeRanges merges overlapping or adjacent ranges into disjoint ones.
func mergeRanges(ranges []Range) []Range {
	rs := make([]Range, len(ranges))
	copy(rs, ranges)
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Start != rs[j].Start {
			return rs[i].Start < rs[j].Start
		}
		return rs[i].End < rs[j].End
	})
	merged := rs[:0]
	for _, r := range rs {
		if len(merged) == 0 || r.Start > merged[len(merged)-1].End {
			merged = append(merged, r)
			continue
		}
		if r.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = r.End
		}
	}
	return merged
}
