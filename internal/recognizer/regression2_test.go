package recognizer

import (
	"strings"
	"testing"
)

// Regression tests for the second round of recognizer fixes: case-insensitive
// labeled fields, field-bound context, composite addresses and reference
// mentions. Each test documents the bug it guards against.

// TestFullNameCaseInsensitive guards the fix that explicitly labeled full-name
// fields support upper, lower and mixed case.
func TestFullNameCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "all caps", in: "ФИО: ИВАНОВ ИВАН ИВАНОВИЧ", want: "ИВАНОВ ИВАН ИВАНОВИЧ"},
		{name: "lowercase", in: "ФИО: иванов иван иванович", want: "иванов иван иванович"},
		{name: "mixed", in: "ФИО: Иванов Иван Иванович", want: "Иванов Иван Иванович"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked, restored := maskAndRestore(t, tt.in)
			if !strings.Contains(masked, "[PII_0]") {
				t.Fatalf("masked %q does not contain a marker", masked)
			}
			if strings.Contains(masked, tt.want) {
				t.Fatalf("masked %q still contains the name", masked)
			}
			if restored != tt.in {
				t.Fatalf("restored %q, want %q", restored, tt.in)
			}
		})
	}
}

// TestSingleNameAfterImya guards the fix that a single given name after the
// field label "имя" is masked.
func TestSingleNameAfterImya(t *testing.T) {
	masked, restored := maskAndRestore(t, "Имя: Анна")
	if !strings.Contains(masked, "[PII_0]") {
		t.Fatalf("masked %q does not contain a marker", masked)
	}
	if strings.Contains(masked, "Анна") {
		t.Fatalf("masked %q still contains the name", masked)
	}
	if restored != "Имя: Анна" {
		t.Fatalf("restored %q, want %q", restored, "Имя: Анна")
	}
}

// TestBirthDateUppercaseMonth guards the fix that a word-form date with an
// uppercase month is recognized.
func TestBirthDateUppercaseMonth(t *testing.T) {
	masked, restored := maskAndRestore(t, "Дата рождения: 12 МАРТА 1990 года")
	if !strings.Contains(masked, "[PII_0]") {
		t.Fatalf("masked %q does not contain a marker", masked)
	}
	if strings.Contains(masked, "12 МАРТА 1990") {
		t.Fatalf("masked %q still contains the date", masked)
	}
	if restored != "Дата рождения: 12 МАРТА 1990 года" {
		t.Fatalf("restored %q", restored)
	}
}

// TestBirthPlaceBareCity guards the fix that a bare capitalized place name is
// accepted after the explicit field label "место рождения".
func TestBirthPlaceBareCity(t *testing.T) {
	masked, restored := maskAndRestore(t, "Место рождения: Казань")
	if !strings.Contains(masked, "[PII_0]") {
		t.Fatalf("masked %q does not contain a marker", masked)
	}
	if strings.Contains(masked, "Казань") {
		t.Fatalf("masked %q still contains the place", masked)
	}
	if restored != "Место рождения: Казань" {
		t.Fatalf("restored %q", restored)
	}
}

// TestNegativeContextFieldBound guards the fix that a negative context word
// (дата, сумма) in a neighbouring field does not suppress a sensitive requisite
// in its own field.
func TestNegativeContextFieldBound(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "passport after date field", in: "Дата выдачи: 01.01.2020; паспорт 4510 123456"},
		{name: "card after amount field", in: "Сумма: 100 рублей; карта: 4111 1111 1111 1111"},
		{name: "cvv after date field", in: "Дата: 01.01.2020; CVV: 123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked, restored := maskAndRestore(t, tt.in)
			if !strings.Contains(masked, "[PII_") {
				t.Fatalf("masked %q does not contain a marker", masked)
			}
			if restored != tt.in {
				t.Fatalf("restored %q, want %q", restored, tt.in)
			}
		})
	}
}

// TestNegativeContextStillSuppresses guards that negative context still
// suppresses order numbers and amounts within the same field.
func TestNegativeContextStillSuppresses(t *testing.T) {
	in := "заказ 4506123456 на сумму 4111111111111111 рублей, дата 12.03.1990"
	masked, restored := maskAndRestore(t, in)
	if masked != in {
		t.Fatalf("masked %q, want unchanged %q", masked, in)
	}
	if restored != in {
		t.Fatalf("restored %q", restored)
	}
}

// TestCompositeAddress guards the fix that a composite address with a
// hyphenated city, a multi-word street and a letter building number stays one
// fragment.
func TestCompositeAddress(t *testing.T) {
	in := "Адрес проживания: г. Санкт-Петербург, ул. Большая Морская, д. 12А, кв. 7"
	masked, restored := maskAndRestore(t, in)
	if !strings.Contains(masked, "[PII_0]") {
		t.Fatalf("masked %q does not contain a marker", masked)
	}
	if strings.Contains(masked, "Санкт-Петербург") || strings.Contains(masked, "Большая Морская") {
		t.Fatalf("masked %q still contains address parts", masked)
	}
	if restored != in {
		t.Fatalf("restored %q, want %q", restored, in)
	}
}

// TestPoetReferenceNotMasked guards the fix that a reference mention of a poet
// is not masked as the client's full name even when a client label appears
// elsewhere in the sentence.
func TestPoetReferenceNotMasked(t *testing.T) {
	in := "Клиент прочитал стихи поэта Александр Пушкин."
	masked, restored := maskAndRestore(t, in)
	if masked != in {
		t.Fatalf("masked %q, want unchanged %q", masked, in)
	}
	if restored != in {
		t.Fatalf("restored %q", restored)
	}
}

// TestClientOwnNameMasked guards that a client who genuinely bears the name is
// masked when the client label introduces it directly, and the label itself is
// not included in the mask.
func TestClientOwnNameMasked(t *testing.T) {
	in := "Клиент Александр Пушкин"
	masked, restored := maskAndRestore(t, in)
	if masked != "Клиент [PII_0]" {
		t.Fatalf("masked %q, want %q", masked, "Клиент [PII_0]")
	}
	if restored != in {
		t.Fatalf("restored %q, want %q", restored, in)
	}
}
