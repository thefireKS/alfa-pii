// Package eval provides a reproducible local evaluation of the recognizers
// against a synthetic dataset. It measures range-level precision and recall,
// sensitive-character coverage, over-masking, category errors and exact
// restoration, and reports per-category results so a large phone set cannot
// hide a failure on addresses.
package eval

import (
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
)

// Range is a byte range [Start, End) into the original UTF-8 text together
// with the expected data type. Byte offsets are the single unit of measure
// shared by the recognizers and the replacement mechanism.
type Range struct {
	Start int
	End   int
	Type  recognizer.Type
}

// Example is one synthetic sample: the original text and the expected
// non-overlapping sensitive ranges. Negative examples carry no expected
// ranges. All ranges are byte offsets into Text.
type Example struct {
	Name     string
	Text     string
	Expected []Range
}

// Negative reports whether the example expects no sensitive data.
func (e Example) Negative() bool { return len(e.Expected) == 0 }

// CategoryMetrics holds range-level and character-level results for one data
// type across a set of examples.
type CategoryMetrics struct {
	Type recognizer.Type

	// Range-level exact-boundary metrics.
	ExpectedRanges int // number of expected ranges of this type
	ActualRanges   int // number of resolved ranges of this type
	TruePositives  int // expected ranges matched exactly by an actual range
	FalseNegatives int // expected ranges with no exact actual match
	FalsePositives int // actual ranges with no exact expected match

	// Character-level metrics (bytes, UTF-8).
	SensitiveBytes int // bytes covered by expected ranges of this type
	CoveredBytes   int // sensitive bytes actually masked
	WrongCatBytes  int // sensitive bytes masked under a different category
	OverMaskBytes  int // bytes masked that are not sensitive at all
}

// Precision returns range-level precision (exact boundary match). It is 0 when
// there are no actual ranges.
func (m CategoryMetrics) Precision() float64 {
	if m.ActualRanges == 0 {
		return 0
	}
	return float64(m.TruePositives) / float64(m.ActualRanges)
}

// Recall returns range-level recall (exact boundary match). It is 0 when there
// are no expected ranges.
func (m CategoryMetrics) Recall() float64 {
	if m.ExpectedRanges == 0 {
		return 0
	}
	return float64(m.TruePositives) / float64(m.ExpectedRanges)
}

// Coverage returns the fraction of sensitive bytes actually masked.
func (m CategoryMetrics) Coverage() float64 {
	if m.SensitiveBytes == 0 {
		return 0
	}
	return float64(m.CoveredBytes) / float64(m.SensitiveBytes)
}

// Result is the outcome of evaluating one example.
type Result struct {
	Example Example

	// Resolved is the set of non-overlapping fragments produced by the full
	// pipeline (all recognizers + Resolve).
	Resolved []recognizer.Fragment

	// Masked is the masked text produced by the Masker.
	Masked string

	// Restored is Masked passed through Restore.
	Restored string

	// Table is the replacement table produced by the Masker.
	Table []masker.Replacement

	// Errors lists concrete problems found for this example.
	Errors []string
}

// Report aggregates metrics across all evaluated examples.
type Report struct {
	ByCategory map[recognizer.Type]*CategoryMetrics

	// Totals across all categories.
	TotalExpectedRanges int
	TotalActualRanges   int
	TotalTruePositives  int
	TotalFalseNegatives int
	TotalFalsePositives int

	TotalSensitiveBytes int
	TotalCoveredBytes   int
	TotalWrongCatBytes  int
	TotalOverMaskBytes  int
	TotalBytes          int

	// Restoration and unchanged-text checks.
	RestoreFailures   int
	UnchangedFailures int

	// Example-level error counts.
	ExamplesWithErrors int
	TotalErrors        int

	// Negative-example counts. A negative example expects no sensitive data;
	// any error on it is an over-masking failure.
	NegativeExamples   int
	NegativeWithErrors int
}
