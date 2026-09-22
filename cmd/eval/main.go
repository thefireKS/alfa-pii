// Command eval builds a reproducible metrics table and error list for the
// recognizers over a synthetic dataset covering all 17 categories. It is the
// single command that verifies recognition quality, exact restoration and
// unchanged text outside masks. The dataset uses a fixed seed, so repeated
// runs reproduce the same result.
//
// Usage:
//
//	go run ./cmd/eval [-out results.txt]
//
// The -out flag writes the full report to a file so actual results are
// persisted for comparison across runs.
package main

import (
	"flag"
	"fmt"
	"os"

	"alfa-hackathon.local/pii/internal/eval"
	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
)

func main() {
	out := flag.String("out", "", "write the report to this file")
	flag.Parse()

	recs := []recognizer.Recognizer{
		recognizer.EmailRecognizer{},
		recognizer.PhoneRecognizer{},
		recognizer.INNRecognizer{},
		recognizer.CardRecognizer{},
		recognizer.PassportRecognizer{},
		recognizer.DepartmentCodeRecognizer{},
		recognizer.DriverLicenseRecognizer{},
		recognizer.PINRecognizer{},
		recognizer.CVVRecognizer{},
		recognizer.FullNameRecognizer{},
		recognizer.BirthDateRecognizer{},
		recognizer.BirthPlaceRecognizer{},
		recognizer.CitizenshipRecognizer{},
		recognizer.PassportAuthorityRecognizer{},
		recognizer.PassportIssueDateRecognizer{},
		recognizer.AddressRecognizer{},
		recognizer.CardHolderNameRecognizer{},
	}
	m := masker.New("PII")
	p := eval.NewPipeline(recs, m)

	dev, held := eval.SplitExamples()
	devResults, devReport := p.Evaluate(dev)
	heldResults, heldReport := p.Evaluate(held)

	var b []byte
	write := func(format string, args ...any) {
		b = append(b, fmt.Sprintf(format, args...)...)
		b = append(b, '\n')
	}

	write("=== PII recognition evaluation (synthetic dataset) ===")
	write("Offset unit: bytes (UTF-8). Character metrics count bytes.")
	write("Seed: %d", eval.Seed())
	write("Dev examples: %d, held-out examples: %d", len(dev), len(held))
	write("")
	writeReport(&b, "DEV SET", devReport)
	writeReport(&b, "HELD-OUT SET", heldReport)
	write("=== Errors (dev set) ===")
	writeErrors(&b, devResults)
	write("=== Errors (held-out set) ===")
	writeErrors(&b, heldResults)

	os.Stdout.Write(b)
	if *out != "" {
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
			os.Exit(1)
		}
	}
}

func writeReport(b *[]byte, title string, rep *eval.Report) {
	write := func(format string, args ...any) {
		*b = append(*b, fmt.Sprintf(format, args...)...)
		*b = append(*b, '\n')
	}
	write("--- %s ---", title)
	write("Ranges: expected=%d actual=%d TP=%d FN=%d FP=%d",
		rep.TotalExpectedRanges, rep.TotalActualRanges,
		rep.TotalTruePositives, rep.TotalFalseNegatives, rep.TotalFalsePositives)
	write("Bytes: sensitive=%d covered=%d wrongCat=%d overMasked=%d total=%d",
		rep.TotalSensitiveBytes, rep.TotalCoveredBytes,
		rep.TotalWrongCatBytes, rep.TotalOverMaskBytes, rep.TotalBytes)
	write("Restoration failures: %d, unchanged-text failures: %d",
		rep.RestoreFailures, rep.UnchangedFailures)
	write("Examples with errors: %d, total errors: %d",
		rep.ExamplesWithErrors, rep.TotalErrors)
	write("Negative examples: %d, negative with errors: %d",
		rep.NegativeExamples, rep.NegativeWithErrors)
	write("")
	write("%-22s %6s %6s %6s %6s %6s %6s %6s %6s %6s",
		"category", "exp", "act", "TP", "FN", "FP", "prec", "rec", "cov%", "over")
	for _, t := range rep.SortedTypes() {
		cm := rep.ByCategory[t]
		write("%-22s %6d %6d %6d %6d %6d %6.2f %6.2f %6.1f %6d",
			t, cm.ExpectedRanges, cm.ActualRanges, cm.TruePositives,
			cm.FalseNegatives, cm.FalsePositives,
			cm.Precision(), cm.Recall(), cm.Coverage()*100, cm.OverMaskBytes)
	}
	write("")
}

func writeErrors(b *[]byte, results []eval.Result) {
	write := func(format string, args ...any) {
		*b = append(*b, fmt.Sprintf(format, args...)...)
		*b = append(*b, '\n')
	}
	count := 0
	for _, r := range results {
		if len(r.Errors) == 0 {
			continue
		}
		count++
		write("[%s] %d error(s)", r.Example.Name, len(r.Errors))
		for _, e := range r.Errors {
			write("    - %s", e)
		}
	}
	if count == 0 {
		write("(none)")
	}
	write("")
}
