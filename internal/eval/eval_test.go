package eval

import (
	"testing"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
)

// testPipeline returns a pipeline wired with all 17 recognizers, matching the
// production wiring in cmd/pii-service.
func testPipeline() *Pipeline {
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
	return NewPipeline(recs, masker.New("PII"))
}

// allTypes is the set of all 17 supported data types.
var allTypes = []recognizer.Type{
	recognizer.Email, recognizer.Phone, recognizer.INN, recognizer.Card,
	recognizer.Passport, recognizer.DepartmentCode, recognizer.DriverLicense,
	recognizer.PIN, recognizer.CVV, recognizer.FullName, recognizer.BirthDate,
	recognizer.BirthPlace, recognizer.Citizenship, recognizer.PassportAuthority,
	recognizer.PassportIssueDate, recognizer.Address, recognizer.CardHolderName,
}

func TestValidateMarkup(t *testing.T) {
	dev, held := SplitExamples()
	all := append(append([]Example{}, dev...), held...)
	if errs := ValidateMarkup(all); len(errs) != 0 {
		for _, e := range errs {
			t.Errorf("markup error in %s [%d,%d): %s", e.Example, e.Start, e.End, e.Reason)
		}
	}
}

func TestValidateMarkupDetectsBadRanges(t *testing.T) {
	bad := []Example{
		{Name: "out_of_bounds", Text: "abc", Expected: []Range{{Start: 0, End: 5, Type: recognizer.Email}}},
		{Name: "not_rune_boundary", Text: "привет", Expected: []Range{{Start: 0, End: 1, Type: recognizer.Email}}},
		{Name: "overlap", Text: "abcdef", Expected: []Range{
			{Start: 0, End: 4, Type: recognizer.Email},
			{Start: 2, End: 6, Type: recognizer.Phone},
		}},
	}
	errs := ValidateMarkup(bad)
	if len(errs) != 3 {
		t.Fatalf("got %d errors, want 3: %+v", len(errs), errs)
	}
}

func TestDatasetCoversAllTypes(t *testing.T) {
	dev, held := SplitExamples()
	all := append(append([]Example{}, dev...), held...)
	covered := make(map[recognizer.Type]bool)
	for _, ex := range all {
		for _, r := range ex.Expected {
			covered[r.Type] = true
		}
	}
	for _, typ := range allTypes {
		if !covered[typ] {
			t.Errorf("dataset does not cover type %s", typ)
		}
	}
}

func TestDatasetHasNegatives(t *testing.T) {
	dev, held := SplitExamples()
	all := append(append([]Example{}, dev...), held...)
	neg := 0
	for _, ex := range all {
		if ex.Negative() {
			neg++
		}
	}
	if neg == 0 {
		t.Fatal("dataset has no negative examples")
	}
}

func TestReproducibleSeed(t *testing.T) {
	a := generateVariants()
	b := generateVariants()
	if len(a) != len(b) {
		t.Fatalf("variant count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Text != b[i].Text || len(a[i].Expected) != len(b[i].Expected) {
			t.Fatalf("variant %d differs between runs", i)
		}
	}
}

func TestMetricsExactMatch(t *testing.T) {
	p := testPipeline()
	ex := Example{
		Name:     "single",
		Text:     "почта a@b.ru",
		Expected: []Range{{Start: len("почта "), End: len("почта a@b.ru"), Type: recognizer.Email}},
	}
	results, rep := p.Evaluate([]Example{ex})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if len(results[0].Errors) != 0 {
		t.Fatalf("unexpected errors: %v", results[0].Errors)
	}
	cm := rep.ByCategory[recognizer.Email]
	if cm.TruePositives != 1 || cm.FalseNegatives != 0 || cm.FalsePositives != 0 {
		t.Fatalf("range metrics wrong: %+v", cm)
	}
	if cm.SensitiveBytes != len("a@b.ru") || cm.CoveredBytes != len("a@b.ru") {
		t.Fatalf("char metrics wrong: %+v", cm)
	}
	if rep.TotalTruePositives != 1 || rep.TotalFalseNegatives != 0 || rep.TotalFalsePositives != 0 {
		t.Fatalf("totals wrong: %+v", rep)
	}
}

func TestMetricsDetectsMiss(t *testing.T) {
	// A recognizer that finds nothing must produce a false negative.
	p := NewPipeline([]recognizer.Recognizer{recognizer.EmailRecognizer{}}, masker.New("PII"))
	ex := Example{
		Name:     "miss",
		Text:     "телефон +7 912 345-67-89",
		Expected: []Range{{Start: len("телефон "), End: len("телефон +7 912 345-67-89"), Type: recognizer.Phone}},
	}
	_, rep := p.Evaluate([]Example{ex})
	cm := rep.ByCategory[recognizer.Phone]
	if cm.FalseNegatives != 1 || cm.TruePositives != 0 {
		t.Fatalf("expected a false negative, got %+v", cm)
	}
	if cm.SensitiveBytes == 0 || cm.CoveredBytes != 0 {
		t.Fatalf("expected uncovered sensitive bytes, got %+v", cm)
	}
}

func TestMetricsDetectsOverMask(t *testing.T) {
	// A recognizer that masks the whole string must produce over-masking.
	p := NewPipeline([]recognizer.Recognizer{wholeStringRecognizer{}}, masker.New("PII"))
	ex := Example{
		Name:     "over",
		Text:     "обычный текст без данных",
		Expected: nil,
	}
	_, rep := p.Evaluate([]Example{ex})
	if rep.TotalOverMaskBytes == 0 {
		t.Fatal("expected over-masking to be detected")
	}
}

// wholeStringRecognizer masks the entire input, used to test over-masking
// detection.
type wholeStringRecognizer struct{}

func (wholeStringRecognizer) Type() recognizer.Type { return recognizer.Email }

func (wholeStringRecognizer) Find(text string) ([]recognizer.Fragment, error) {
	return []recognizer.Fragment{{Type: recognizer.Email, Start: 0, End: len(text)}}, nil
}

func TestMetricsDetectsWrongCategory(t *testing.T) {
	// A recognizer that reports the right range but the wrong type must be
	// counted as a category error, not as a miss.
	p := NewPipeline([]recognizer.Recognizer{wrongCatRecognizer{}}, masker.New("PII"))
	ex := Example{
		Name:     "wrongcat",
		Text:     "почта a@b.ru",
		Expected: []Range{{Start: len("почта "), End: len("почта a@b.ru"), Type: recognizer.Email}},
	}
	_, rep := p.Evaluate([]Example{ex})
	cm := rep.ByCategory[recognizer.Email]
	if cm.SensitiveBytes == 0 || cm.WrongCatBytes != cm.SensitiveBytes {
		t.Fatalf("expected all sensitive bytes to be wrong-category, got %+v", cm)
	}
	if cm.CoveredBytes != cm.SensitiveBytes {
		t.Fatalf("bytes should still be covered, got %+v", cm)
	}
}

// wrongCatRecognizer reports the email range but labels it as a phone, to test
// category-error accounting.
type wrongCatRecognizer struct{}

func (wrongCatRecognizer) Type() recognizer.Type { return recognizer.Phone }

func (wrongCatRecognizer) Find(text string) ([]recognizer.Fragment, error) {
	start := len("почта ")
	return []recognizer.Fragment{{Type: recognizer.Phone, Start: start, End: len(text)}}, nil
}

func TestExactRestoration(t *testing.T) {
	p := testPipeline()
	dev, held := SplitExamples()
	all := append(append([]Example{}, dev...), held...)
	for _, ex := range all {
		results, _ := p.Evaluate([]Example{ex})
		if results[0].Restored != ex.Text {
			t.Errorf("%s: restoration mismatch", ex.Name)
		}
	}
}

func TestUnchangedOutsideMasks(t *testing.T) {
	p := testPipeline()
	dev, held := SplitExamples()
	all := append(append([]Example{}, dev...), held...)
	for _, ex := range all {
		results, _ := p.Evaluate([]Example{ex})
		if len(results[0].Errors) > 0 {
			// unchanged-text failures are reported as errors; check none is a
			// "text outside masks changed" error.
			for _, e := range results[0].Errors {
				if e == "text outside masks changed" {
					t.Errorf("%s: text outside masks changed", ex.Name)
				}
			}
		}
	}
}

// TestPoetReferenceNotMasked guards the fix that a reference mention of a
// person (for example a poet) is not masked as the client's full name, even
// when a client label appears elsewhere in the sentence. The reference role
// marker "поэт" suppresses masking; a client who genuinely bears the name is
// still masked when the client label introduces it directly.
func TestPoetReferenceNotMasked(t *testing.T) {
	p := testPipeline()
	// Clean reference without a client label is not masked.
	clean := Example{Name: "clean", Text: "поэт Александр Пушкин", Expected: nil}
	cleanRes, _ := p.Evaluate([]Example{clean})
	if len(cleanRes[0].Errors) != 0 {
		t.Fatalf("clean reference should not be masked: %v", cleanRes[0].Errors)
	}
	// Reference mention with a client label elsewhere in the sentence is not
	// masked either: the name is introduced by the reference marker "поэта".
	labeled := Example{Name: "labeled", Text: "Клиент прочитал стихи поэта Александр Пушкин", Expected: nil}
	labeledRes, _ := p.Evaluate([]Example{labeled})
	if len(labeledRes[0].Errors) != 0 {
		t.Fatalf("reference mention should not be masked: %v", labeledRes[0].Errors)
	}
	// A client who genuinely bears the name is masked when the client label
	// introduces it directly.
	client := Example{Name: "client", Text: "Клиент Александр Пушкин", Expected: []Range{
		{Start: len("Клиент "), End: len("Клиент Александр Пушкин"), Type: recognizer.FullName},
	}}
	clientRes, _ := p.Evaluate([]Example{client})
	if len(clientRes[0].Errors) != 0 {
		t.Fatalf("client's own name should be masked: %v", clientRes[0].Errors)
	}
}
