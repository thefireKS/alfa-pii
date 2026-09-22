package recognizer

import "testing"

func TestFullNameFind(t *testing.T) {
	r := FullNameRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "labeled full name", in: "ФИО: Иванов Иван Иванович", want: []string{"Иванов Иван Иванович"}},
		{name: "client name", in: "клиент Александр Пушкин", want: []string{"Александр Пушкин"}},
		{name: "initials form", in: "ФИО: Иванов И. И.", want: []string{"Иванов И. И."}},
		{name: "initials first", in: "ФИО: И. И. Иванов", want: []string{"И. И. Иванов"}},
		{name: "surname field", in: "фамилия: Иванов", want: []string{"Иванов"}},
		{name: "patronymic field", in: "отчество: Иванович", want: []string{"Иванович"}},
		{name: "poet reference not masked", in: "поэт Александр Пушкин", want: nil},
		{name: "bare name not masked", in: "Александр Пушкин", want: nil},
		{name: "all caps abbreviation not name", in: "паспорт выдан ОУФМС России по г. Москве", want: nil},
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

func TestBirthDateFind(t *testing.T) {
	r := BirthDateRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "numeric", in: "дата рождения: 12.03.1990", want: []string{"12.03.1990"}},
		{name: "word form", in: "дата рождения 12 марта 1990 года", want: []string{"12 марта 1990 года"}},
		{name: "born label", in: "родился 12.03.1990", want: []string{"12.03.1990"}},
		{name: "bare date not masked", in: "12.03.1990", want: nil},
		{name: "issue date not birth date", in: "дата выдачи: 15.04.2015", want: nil},
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

func TestBirthPlaceFind(t *testing.T) {
	r := BirthPlaceRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "labeled city", in: "место рождения: г. Москва", want: []string{"г. Москва"}},
		{name: "born in city", in: "родился в г. Москве", want: []string{"г. Москве"}},
		{name: "born in bare city", in: "родился в Москве", want: []string{"в Москве"}},
		{name: "bare place not masked", in: "Москва", want: nil},
		{name: "name after born not place", in: "родился Иван Иванов", want: nil},
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

func TestCitizenshipFind(t *testing.T) {
	r := CitizenshipRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "labeled federation", in: "гражданство: Российская Федерация", want: []string{"Российская Федерация"}},
		{name: "citizen rf", in: "гражданин РФ", want: []string{"РФ"}},
		{name: "citizen country", in: "гражданин Казахстана", want: []string{"Казахстана"}},
		{name: "bare country not masked", in: "Российская Федерация", want: nil},
		{name: "person name not citizenship", in: "гражданин Иванов Иван", want: nil},
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

func TestPassportAuthorityFind(t *testing.T) {
	r := PassportAuthorityRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "issued by", in: "паспорт выдан ОУФМС России по г. Москве", want: []string{"ОУФМС России по г. Москве"}},
		{name: "issued by department", in: "выдан Отделом УФМС России по г. Москве", want: []string{"Отделом УФМС России по г. Москве"}},
		{name: "bare authority not masked", in: "ОУФМС России по г. Москве", want: nil},
		{name: "issue date not authority", in: "выдан 12.03.2015", want: nil},
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

func TestPassportIssueDateFind(t *testing.T) {
	r := PassportIssueDateRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "labeled", in: "дата выдачи: 15.04.2015", want: []string{"15.04.2015"}},
		{name: "issued date", in: "выдан 12.03.2015", want: []string{"12.03.2015"}},
		{name: "bare date not masked", in: "15.04.2015", want: nil},
		{name: "birth date not issue date", in: "дата рождения: 12.03.1990", want: nil},
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

func TestAddressFind(t *testing.T) {
	r := AddressRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "client address", in: "адрес клиента: г. Москва, ул. Ленина, д. 5, кв. 10", want: []string{"г. Москва, ул. Ленина, д. 5, кв. 10"}},
		{name: "residence address", in: "проживает по адресу: г. Москва, ул. Ленина, д. 5", want: []string{"г. Москва, ул. Ленина, д. 5"}},
		{name: "branch address not masked", in: "адрес отделения: г. Москва, ул. Тверская, д. 1", want: nil},
		{name: "bare address not masked", in: "г. Москва, ул. Ленина, д. 5", want: nil},
		{name: "branch and client in one sentence", in: "адрес отделения: г. Москва, ул. Тверская, д. 1; адрес клиента: г. Москва, ул. Ленина, д. 5", want: []string{"г. Москва, ул. Ленина, д. 5"}},
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

func TestCardHolderNameFind(t *testing.T) {
	r := CardHolderNameRecognizer{}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "latin upper", in: "держатель карты IVAN IVANOV", want: []string{"IVAN IVANOV"}},
		{name: "latin title", in: "card holder Ivan Ivanov", want: []string{"Ivan Ivanov"}},
		{name: "cyrillic", in: "имя на карте Иван Иванов", want: []string{"Иван Иванов"}},
		{name: "bare name not masked", in: "IVAN IVANOV", want: nil},
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
