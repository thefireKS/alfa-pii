package recognizer

import "testing"

// Regression tests for recognizer fixes driven by held-out evaluation
// examples. Each test documents the bug it guards against.

// TestBirthPlaceHyphenatedCity guards the fix that allowed hyphenated city
// names such as "Санкт-Петербург" in a place designation.
func TestBirthPlaceHyphenatedCity(t *testing.T) {
	r := BirthPlaceRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "hyphenated city", in: "место рождения: г. Санкт-Петербург", want: []string{"г. Санкт-Петербург"}},
		{name: "hyphenated born", in: "родился в Санкт-Петербурге", want: []string{"в Санкт-Петербурге"}},
		{name: "lowercase hyphen part", in: "родился в Ростове-на-Дону", want: []string{"в Ростове-на-Дону"}},
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

// TestCitizenshipRepublicBelarus guards the fix that added the full country
// name "Республика Беларусь" to the citizenship dictionary. The recognizer may
// report both the short and the full form; the resolved output must be the
// full form.
func TestCitizenshipRepublicBelarus(t *testing.T) {
	r := CitizenshipRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "republic belarus", in: "гражданство: Республика Беларусь", want: []string{"Республика Беларусь"}},
		{name: "republic belarus genitive", in: "гражданин Республики Беларусь", want: []string{"Республики Беларусь"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frags, err := r.Find(tt.in)
			if err != nil {
				t.Fatalf("find: %v", err)
			}
			resolved, err := Resolve(tt.in, frags)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got := make([]string, 0, len(resolved))
			for _, f := range resolved {
				got = append(got, tt.in[f.Start:f.End])
			}
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
