package recognizer

import "testing"

// findStrings runs a recognizer and returns the matched substrings in order.
func findStrings(t *testing.T, r Recognizer, in string) []string {
	t.Helper()
	frags, err := r.Find(in)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	got := make([]string, 0, len(frags))
	for _, f := range frags {
		got = append(got, in[f.Start:f.End])
	}
	return got
}

func TestPhoneFind(t *testing.T) {
	r := PhoneRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "plus spaced", in: "тел +7 (912) 345-67-89", want: []string{"+7 (912) 345-67-89"}},
		{name: "eight prefix", in: "8 (912) 345-67-89", want: []string{"8 (912) 345-67-89"}},
		{name: "plus compact", in: "+79123456789", want: []string{"+79123456789"}},
		{name: "eight compact", in: "89123456789", want: []string{"89123456789"}},
		{name: "bare seven needs context", in: "79123456789", want: nil},
		{name: "bare seven with context", in: "телефон 79123456789", want: []string{"79123456789"}},
		{name: "order number not phone", in: "заказ 791234567890", want: nil},
		{name: "short number not phone", in: "номер 12345", want: nil},
		{name: "cyrillic boundary", in: "позвоните +7 912 345-67-89 пожалуйста", want: []string{"+7 912 345-67-89"}},
		{name: "punctuation boundary", in: "тел.: +7 912 345-67-89, спасибо", want: []string{"+7 912 345-67-89"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestINNFind(t *testing.T) {
	r := INNRecognizer{}
	// 10-digit INN with valid checksum: 7707083893
	// 12-digit INN with valid checksums: 500100732259
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "valid 10", in: "ИНН 7707083893", want: []string{"7707083893"}},
		{name: "valid 12", in: "ИНН 500100732259", want: []string{"500100732259"}},
		{name: "valid 10 bare", in: "7707083893", want: []string{"7707083893"}},
		{name: "invalid checksum bare", in: "7707083894", want: nil},
		{name: "invalid checksum signed", in: "ИНН 7707083894", want: []string{"7707083894"}},
		{name: "wrong length", in: "ИНН 770708389", want: nil},
		{name: "order number", in: "заказ 7707083893", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestCardFind(t *testing.T) {
	r := CardRecognizer{}
	// 4111111111111111 passes Luhn.
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "spaced", in: "карта 4111 1111 1111 1111", want: []string{"4111 1111 1111 1111"}},
		{name: "hyphenated", in: "4111-1111-1111-1111", want: []string{"4111-1111-1111-1111"}},
		{name: "compact", in: "4111111111111111", want: []string{"4111111111111111"}},
		{name: "invalid luhn bare", in: "4111111111111112", want: nil},
		{name: "invalid luhn signed", in: "карта 4111111111111112", want: []string{"4111111111111112"}},
		{name: "too short", in: "карта 4111 1111", want: nil},
		{name: "order number", in: "заказ 4111111111111111", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestPassportFind(t *testing.T) {
	r := PassportRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "spaced", in: "паспорт 4506 123456", want: []string{"4506 123456"}},
		{name: "hyphenated", in: "серия 4506-123456", want: []string{"4506-123456"}},
		{name: "compact signed", in: "паспорт 4506123456", want: []string{"4506123456"}},
		{name: "bare ambiguous", in: "4506123456", want: nil},
		{name: "order number", in: "заказ 4506123456", want: nil},
		{name: "wrong length", in: "паспорт 4506 12345", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDepartmentCodeFind(t *testing.T) {
	r := DepartmentCodeRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "signed", in: "код подразделения 770-123", want: []string{"770-123"}},
		{name: "bare ambiguous", in: "770-123", want: nil},
		{name: "no hyphen", in: "код подразделения 770123", want: nil},
		{name: "order number", in: "заказ 770-123", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDriverLicenseFind(t *testing.T) {
	r := DriverLicenseRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "spaced", in: "водительское удостоверение 7701 123456", want: []string{"7701 123456"}},
		{name: "compact signed", in: "права 7701123456", want: []string{"7701123456"}},
		{name: "bare ambiguous", in: "7701123456", want: nil},
		{name: "order number", in: "заказ 7701123456", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestPINFind(t *testing.T) {
	r := PINRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "signed", in: "ПИН 1234", want: []string{"1234"}},
		{name: "pin-code", in: "пин-код 5678", want: []string{"5678"}},
		{name: "bare ambiguous", in: "1234", want: nil},
		{name: "order number", in: "заказ 1234", want: nil},
		{name: "wrong length", in: "ПИН 12345", want: nil},
		{name: "word boundary", in: "спинка 1234", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestCVVFind(t *testing.T) {
	r := CVVRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "signed", in: "CVV 123", want: []string{"123"}},
		{name: "russian", in: "код безопасности 456", want: []string{"456"}},
		{name: "bare ambiguous", in: "123", want: nil},
		{name: "order number", in: "заказ 123", want: nil},
		{name: "wrong length", in: "CVV 1234", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStrings(t, r, tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}
