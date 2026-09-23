package masker

import (
	"fmt"
	"strings"
	"testing"
)

// largeText builds a text of approximately n runes by repeating a filler word
// with a single email at the end, mirroring the large-text load scenario.
func largeText(n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString("клиент ")
	}
	b.WriteString("email a.b@example.test")
	return b.String()
}

// manyEntitiesText builds a text of approximately n runes made of many emails.
func manyEntitiesText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "email user%d@example.test", i)
	}
	return b.String()
}

// BenchmarkMaskLargeText measures the sequential replacement of a single entity
// in a ~100k-token text. It verifies the marker scan and the strings.Builder
// replacement stay linear in the text length.
func BenchmarkMaskLargeText(b *testing.B) {
	m := New("PII")
	text := largeText(400_000)
	ranges := []Range{{Start: len(text) - len("a.b@example.test"), End: len(text)}}
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		masked, table := m.Mask(text, ranges)
		if len(table) != 1 {
			b.Fatalf("table = %d, want 1", len(table))
		}
		_ = masked
	}
}

// BenchmarkMaskManyEntities measures the sequential replacement of many emails
// in a large text, exercising the marker allocation and the fragment list.
func BenchmarkMaskManyEntities(b *testing.B) {
	m := New("PII")
	text := manyEntitiesText(400_000)
	// Build the ranges for every email occurrence.
	var ranges []Range
	idx := 0
	for {
		pos := strings.Index(text[idx:], "@example.test")
		if pos < 0 {
			break
		}
		start := idx + pos - len("user12345")
		end := idx + pos + len("@example.test")
		ranges = append(ranges, Range{Start: start, End: end})
		idx = end
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		masked, table := m.Mask(text, ranges)
		if len(table) != len(ranges) {
			b.Fatalf("table = %d, want %d", len(table), len(ranges))
		}
		_ = masked
	}
}

// BenchmarkUsedMarkersLargeText measures the single-pass marker scan over a
// large text, verifying it is linear and does not rescan per marker.
func BenchmarkUsedMarkersLargeText(b *testing.B) {
	m := New("PII")
	text := largeText(400_000)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		used := m.usedMarkers(text)
		if len(used) != 0 {
			b.Fatalf("used = %d, want 0", len(used))
		}
	}
}

// BenchmarkRestoreManyEntities measures restoration of a text with many markers,
// verifying the marker substitution is a single left-to-right pass.
func BenchmarkRestoreManyEntities(b *testing.B) {
	m := New("PII")
	text := manyEntitiesText(400_000)
	var ranges []Range
	idx := 0
	for {
		pos := strings.Index(text[idx:], "@example.test")
		if pos < 0 {
			break
		}
		start := idx + pos - len("user12345")
		end := idx + pos + len("@example.test")
		ranges = append(ranges, Range{Start: start, End: end})
		idx = end
	}
	masked, table := m.Mask(text, ranges)
	b.ReportAllocs()
	b.SetBytes(int64(len(masked)))
	for i := 0; i < b.N; i++ {
		got := Restore(masked, table)
		if got != text {
			b.Fatalf("restore mismatch")
		}
	}
}