package eval

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
)

// Pipeline runs the full masking pipeline over a text: all recognizers,
// overlap resolution and replacement. It returns the resolved fragments, the
// masked text and the replacement table.
type Pipeline struct {
	recognizers []recognizer.Recognizer
	masker      *masker.Masker
}

// NewPipeline wires the recognizers and masker used by the evaluation.
func NewPipeline(recs []recognizer.Recognizer, m *masker.Masker) *Pipeline {
	return &Pipeline{recognizers: recs, masker: m}
}

// Mask runs the pipeline and returns resolved fragments and masked text.
func (p *Pipeline) Mask(text string) ([]recognizer.Fragment, string, []masker.Replacement, error) {
	var frags []recognizer.Fragment
	for _, r := range p.recognizers {
		fs, err := r.Find(text)
		if err != nil {
			return nil, "", nil, fmt.Errorf("recognize %s: %w", r.Type(), err)
		}
		frags = append(frags, fs...)
	}
	resolved, err := recognizer.Resolve(text, frags)
	if err != nil {
		return nil, "", nil, err
	}
	ranges := make([]masker.Range, 0, len(resolved))
	for _, f := range resolved {
		ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
	}
	masked, table := p.masker.Mask(text, ranges)
	return resolved, masked, table, nil
}

// Evaluate runs the pipeline over every example and aggregates metrics. It
// returns the per-example results and the aggregate report.
func (p *Pipeline) Evaluate(examples []Example) ([]Result, *Report) {
	rep := &Report{ByCategory: make(map[recognizer.Type]*CategoryMetrics)}
	results := make([]Result, 0, len(examples))
	for _, ex := range examples {
		res := p.evaluateOne(ex)
		results = append(results, res)
		rep.accumulate(res)
	}
	return results, rep
}

// evaluateOne evaluates a single example and records concrete errors.
func (p *Pipeline) evaluateOne(ex Example) Result {
	res := Result{Example: ex}
	resolved, masked, table, err := p.Mask(ex.Text)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("pipeline error: %v", err))
		return res
	}
	res.Resolved = resolved
	res.Masked = masked
	res.Restored = masker.Restore(masked, table)
	res.Table = table

	// Exact restoration check.
	if res.Restored != ex.Text {
		res.Errors = append(res.Errors, "restoration mismatch: masked+restore != original")
	}

	// Unchanged text outside masks: removing the markers from the masked text
	// must leave exactly the original text with the masked bytes removed.
	// Because masking shifts byte positions, this skeleton comparison is the
	// correct way to verify that no byte outside a mask was altered.
	if !unchangedOK(ex.Text, masked, resolved, table) {
		res.Errors = append(res.Errors, "text outside masks changed")
	}

	// Range-level exact-boundary matching per category.
	expectedByCat := groupRanges(ex.Expected)
	actualByCat := groupFragments(resolved)
	for typ, exp := range expectedByCat {
		act := actualByCat[typ]
		tp := countExactMatches(exp, act)
		fn := len(exp) - tp
		fp := len(act) - tp
		if fn > 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %d expected range(s) not matched exactly", typ, fn))
		}
		if fp > 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %d extra range(s) with no exact expected match", typ, fp))
		}
	}
	// Extra actual ranges of categories with no expected ranges are false
	// positives too.
	for typ, act := range actualByCat {
		if _, ok := expectedByCat[typ]; ok {
			continue
		}
		if len(act) > 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %d unexpected range(s)", typ, len(act)))
		}
	}

	// Character-level checks.
	expectedCover := byteCoverage(ex.Text, toFragments(ex.Expected))
	actual := byteCoverage(ex.Text, resolved)
	// Category per byte from expected ranges.
	expectedCat := byteCategory(ex.Text, toFragments(ex.Expected))
	actualCat := byteCategory(ex.Text, resolved)

	for i := 0; i < len(ex.Text); i++ {
		if expectedCover[i] {
			if actual[i] {
				// Sensitive byte masked.
				if actualCat[i] != expectedCat[i] {
					res.Errors = append(res.Errors, fmt.Sprintf("byte %d masked under wrong category %s (want %s)", i, actualCat[i], expectedCat[i]))
				}
			} else {
				res.Errors = append(res.Errors, fmt.Sprintf("sensitive byte %d left unmasked", i))
			}
		} else if actual[i] {
			res.Errors = append(res.Errors, fmt.Sprintf("non-sensitive byte %d over-masked", i))
		}
	}

	return res
}

// accumulate folds one example result into the report.
func (rep *Report) accumulate(res Result) {
	ex := res.Example
	expectedCover := byteCoverage(ex.Text, toFragments(ex.Expected))
	actual := byteCoverage(ex.Text, res.Resolved)
	expectedCat := byteCategory(ex.Text, toFragments(ex.Expected))
	actualCat := byteCategory(ex.Text, res.Resolved)

	// Range-level per category.
	expectedByCat := groupRanges(ex.Expected)
	actualByCat := groupFragments(res.Resolved)
	for typ, exp := range expectedByCat {
		cm := rep.cat(typ)
		cm.ExpectedRanges += len(exp)
		act := actualByCat[typ]
		tp := countExactMatches(exp, act)
		cm.TruePositives += tp
		cm.FalseNegatives += len(exp) - tp
		rep.TotalTruePositives += tp
		rep.TotalFalseNegatives += len(exp) - tp
	}
	for typ, act := range actualByCat {
		cm := rep.cat(typ)
		cm.ActualRanges += len(act)
		exp := expectedByCat[typ]
		tp := countExactMatches(exp, act)
		cm.FalsePositives += len(act) - tp
		rep.TotalFalsePositives += len(act) - tp
	}

	// Character-level per category.
	for i := 0; i < len(ex.Text); i++ {
		if expectedCover[i] {
			cm := rep.cat(expectedCat[i])
			cm.SensitiveBytes++
			rep.TotalSensitiveBytes++
			if actual[i] {
				cm.CoveredBytes++
				rep.TotalCoveredBytes++
				if actualCat[i] != expectedCat[i] {
					cm.WrongCatBytes++
					rep.TotalWrongCatBytes++
				}
			}
		} else if actual[i] {
			cm := rep.cat(actualCat[i])
			cm.OverMaskBytes++
			rep.TotalOverMaskBytes++
		}
	}
	rep.TotalBytes += len(ex.Text)

	// Totals.
	rep.TotalExpectedRanges += len(ex.Expected)
	rep.TotalActualRanges += len(res.Resolved)
	if res.Restored != ex.Text {
		rep.RestoreFailures++
	}
	if unchangedFailure(res) {
		rep.UnchangedFailures++
	}
	if len(res.Errors) > 0 {
		rep.ExamplesWithErrors++
		rep.TotalErrors += len(res.Errors)
	}
	if ex.Negative() {
		rep.NegativeExamples++
		if len(res.Errors) > 0 {
			rep.NegativeWithErrors++
		}
	}
}

func (rep *Report) cat(t recognizer.Type) *CategoryMetrics {
	cm, ok := rep.ByCategory[t]
	if !ok {
		cm = &CategoryMetrics{Type: t}
		rep.ByCategory[t] = cm
	}
	return cm
}

// unchangedFailure reports whether the masked text altered any byte outside a
// mask. It uses the skeleton comparison so byte-position shifts from masking
// do not produce false positives.
func unchangedFailure(res Result) bool {
	return !unchangedOK(res.Example.Text, res.Masked, res.Resolved, res.Table)
}

// unchangedOK verifies that removing the markers from masked leaves exactly the
// original text with the masked bytes removed.
func unchangedOK(text, masked string, resolved []recognizer.Fragment, table []masker.Replacement) bool {
	cover := byteCoverage(text, resolved)
	var exp []byte
	for i := 0; i < len(text); i++ {
		if !cover[i] {
			exp = append(exp, text[i])
		}
	}
	got := masked
	for _, r := range table {
		got = strings.ReplaceAll(got, r.Marker, "")
	}
	return string(exp) == got
}

// toFragments converts expected ranges to fragments for coverage helpers.
func toFragments(ranges []Range) []recognizer.Fragment {
	out := make([]recognizer.Fragment, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, recognizer.Fragment{Type: r.Type, Start: r.Start, End: r.End})
	}
	return out
}

// groupRanges groups expected ranges by type.
func groupRanges(ranges []Range) map[recognizer.Type][]Range {
	out := make(map[recognizer.Type][]Range)
	for _, r := range ranges {
		out[r.Type] = append(out[r.Type], r)
	}
	return out
}

// groupFragments groups resolved fragments by type.
func groupFragments(frags []recognizer.Fragment) map[recognizer.Type][]recognizer.Fragment {
	out := make(map[recognizer.Type][]recognizer.Fragment)
	for _, f := range frags {
		out[f.Type] = append(out[f.Type], f)
	}
	return out
}

// countExactMatches counts expected ranges that have an exact boundary match
// among the actual ranges of the same type. Each actual range is consumed at
// most once.
func countExactMatches(expected []Range, actual []recognizer.Fragment) int {
	used := make([]bool, len(actual))
	count := 0
	for _, e := range expected {
		for j, a := range actual {
			if used[j] {
				continue
			}
			if a.Start == e.Start && a.End == e.End {
				used[j] = true
				count++
				break
			}
		}
	}
	return count
}

// byteCoverage returns a boolean slice marking bytes covered by any fragment.
func byteCoverage(text string, frags []recognizer.Fragment) []bool {
	cover := make([]bool, len(text))
	for _, f := range frags {
		for i := f.Start; i < f.End && i < len(text); i++ {
			cover[i] = true
		}
	}
	return cover
}

// byteCategory returns the type covering each byte, or "" if none. When
// multiple fragments cover a byte, the highest-priority type wins, matching
// the resolution semantics.
func byteCategory(text string, frags []recognizer.Fragment) []recognizer.Type {
	cat := make([]recognizer.Type, len(text))
	for _, f := range frags {
		for i := f.Start; i < f.End && i < len(text); i++ {
			if cat[i] == "" || recognizer.Priority(f.Type) > recognizer.Priority(cat[i]) {
				cat[i] = f.Type
			}
		}
	}
	return cat
}

// SortedTypes returns the data types present in the report in a stable order.
func (rep *Report) SortedTypes() []recognizer.Type {
	types := make([]recognizer.Type, 0, len(rep.ByCategory))
	for t := range rep.ByCategory {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types
}

// MarkupError describes an invalid expected range in an example.
type MarkupError struct {
	Example string
	Start   int
	End     int
	Reason  string
}

// ValidateMarkup checks that every expected range in the examples is within
// the text, lies on UTF-8 rune boundaries, and does not overlap another range.
// It returns a list of problems; an empty list means the markup is valid.
func ValidateMarkup(examples []Example) []MarkupError {
	var errs []MarkupError
	for _, ex := range examples {
		n := len(ex.Text)
		for i, r := range ex.Expected {
			if r.Start < 0 || r.End > n || r.Start > r.End {
				errs = append(errs, MarkupError{ex.Name, r.Start, r.End, "range out of bounds"})
				continue
			}
			if !utf8.ValidString(ex.Text[r.Start:r.End]) {
				errs = append(errs, MarkupError{ex.Name, r.Start, r.End, "range not on rune boundary"})
			}
			for j := 0; j < i; j++ {
				prev := ex.Expected[j]
				if r.Start < prev.End && prev.Start < r.End {
					errs = append(errs, MarkupError{ex.Name, r.Start, r.End, "range overlaps another range"})
					break
				}
			}
		}
	}
	return errs
}
