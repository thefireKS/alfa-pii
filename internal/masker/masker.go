// Package masker replaces sensitive fragments with markers and restores the
// original text from a replacement table. Markers are chosen so they never
// collide with text that already exists in the input.
package masker

import (
	"fmt"
	"sort"
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
	table := make([]Replacement, 0, len(merged))
	var b strings.Builder
	last := 0
	for i, r := range merged {
		b.WriteString(text[last:r.Start])
		marker := m.nextMarker(text, i)
		b.WriteString(marker)
		table = append(table, Replacement{Marker: marker, Original: text[r.Start:r.End]})
		last = r.End
	}
	b.WriteString(text[last:])
	return b.String(), table
}

// Restore replaces every marker in masked with its original value from the
// table. Markers not present in the table are left untouched.
func Restore(masked string, table []Replacement) string {
	if len(table) == 0 {
		return masked
	}
	// Replace longest markers first so a marker that is a prefix of another
	// does not shadow it.
	sorted := make([]Replacement, len(table))
	copy(sorted, table)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i].Marker) > len(sorted[j].Marker)
	})
	out := masked
	for _, r := range sorted {
		out = strings.ReplaceAll(out, r.Marker, r.Original)
	}
	return out
}

// nextMarker returns a marker of the form prefix_N that does not occur in
// text, so it cannot collide with existing content.
func (m *Masker) nextMarker(text string, n int) string {
	for {
		marker := fmt.Sprintf("[%s_%d]", m.prefix, n)
		if !strings.Contains(text, marker) {
			return marker
		}
		n++
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
