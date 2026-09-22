package eval

import (
	"math/rand"
	"strings"

	"alfa-hackathon.local/pii/internal/recognizer"
)

// generatorSeed is fixed so the generated variant set is reproducible.
const generatorSeed = 20260922

// Seed returns the fixed generator seed used to build the variant set.
func Seed() int { return generatorSeed }

// genVariant is a template that produces a full example text and the sensitive
// value(s) it contains. The value is located in the text by the ex builder, so
// byte ranges stay correct under case and separator variations.
type genVariant struct {
	text  string
	wants []want
}

// generateVariants returns a reproducible set of additional examples produced
// from per-category templates with a fixed seed. It covers case, separator,
// Unicode and context variations that are not in the curated sets.
func generateVariants() []Example {
	rng := rand.New(rand.NewSource(generatorSeed))
	var out []Example
	for _, g := range variantGroups {
		// Shuffle deterministically so the split into dev/held-out is stable.
		shuffled := make([]genVariant, len(g.variants))
		copy(shuffled, g.variants)
		rng.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		// Split: first half to dev, second half to held-out.
		half := len(shuffled) / 2
		for i, v := range shuffled {
			name := g.name + "_gen_" + itoa(i)
			ex := ex(name, v.text, v.wants...)
			if i < half {
				out = append(out, ex)
			}
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// variantGroup groups generated variants by category so the dev/held-out split
// keeps every category represented in both sets.
type variantGroup struct {
	name     string
	variants []genVariant
}

var variantGroups = []variantGroup{
	{
		name: "email",
		variants: []genVariant{
			{text: "почта: a.b@example.com", wants: []want{{"a.b@example.com", recognizer.Email}}},
			{text: "EMAIL user@mail.ru", wants: []want{{"user@mail.ru", recognizer.Email}}},
			{text: "напишите на почту 👋 ivanov@example.org", wants: []want{{"ivanov@example.org", recognizer.Email}}},
			{text: "контакт: first.last@sub.example.io", wants: []want{{"first.last@sub.example.io", recognizer.Email}}},
			{text: "почта без точки a@b не маскируется", wants: nil},
		},
	},
	{
		name: "phone",
		variants: []genVariant{
			{text: "тел +7 912 345 67 89", wants: []want{{"+7 912 345 67 89", recognizer.Phone}}},
			{text: "МОБИЛЬНЫЙ 8-912-345-67-89", wants: []want{{"8-912-345-67-89", recognizer.Phone}}},
			{text: "позвоните по телефону +7(912)345-67-89", wants: []want{{"+7(912)345-67-89", recognizer.Phone}}},
			{text: "тел: 79123456789", wants: []want{{"79123456789", recognizer.Phone}}},
			{text: "номер заказа 791234567890", wants: nil},
		},
	},
	{
		name: "inn",
		variants: []genVariant{
			{text: "ИНН 7707083893", wants: []want{{"7707083893", recognizer.INN}}},
			{text: "инн 500100732259", wants: []want{{"500100732259", recognizer.INN}}},
			{text: "ИНН: 7707083894", wants: []want{{"7707083894", recognizer.INN}}},
			{text: "7707083893", wants: []want{{"7707083893", recognizer.INN}}},
			{text: "заказ 7707083893", wants: nil},
		},
	},
	{
		name: "card",
		variants: []genVariant{
			{text: "карта 4111-1111-1111-1111", wants: []want{{"4111-1111-1111-1111", recognizer.Card}}},
			{text: "CARD 4111111111111111", wants: []want{{"4111111111111111", recognizer.Card}}},
			{text: "номер карты 4111 1111 1111 1111", wants: []want{{"4111 1111 1111 1111", recognizer.Card}}},
			{text: "карта 4111111111111112", wants: []want{{"4111111111111112", recognizer.Card}}},
			{text: "заказ 4111111111111111", wants: nil},
		},
	},
	{
		name: "passport",
		variants: []genVariant{
			{text: "паспорт 4506-123456", wants: []want{{"4506-123456", recognizer.Passport}}},
			{text: "СЕРИЯ 4506 123456", wants: []want{{"4506 123456", recognizer.Passport}}},
			{text: "паспорт 4506123456", wants: []want{{"4506123456", recognizer.Passport}}},
			{text: "4506123456", wants: nil},
		},
	},
	{
		name: "department",
		variants: []genVariant{
			{text: "код подразделения 123-456", wants: []want{{"123-456", recognizer.DepartmentCode}}},
			{text: "Код подразделения 770-123", wants: []want{{"770-123", recognizer.DepartmentCode}}},
			{text: "770-123", wants: nil},
		},
	},
	{
		name: "driver",
		variants: []genVariant{
			{text: "водительское удостоверение 7701-123456", wants: []want{{"7701-123456", recognizer.DriverLicense}}},
			{text: "ПРАВА 7701123456", wants: []want{{"7701123456", recognizer.DriverLicense}}},
			{text: "7701123456", wants: nil},
		},
	},
	{
		name: "pin",
		variants: []genVariant{
			{text: "ПИН 1234", wants: []want{{"1234", recognizer.PIN}}},
			{text: "пин-код 5678", wants: []want{{"5678", recognizer.PIN}}},
			{text: "PIN 4321", wants: []want{{"4321", recognizer.PIN}}},
			{text: "1234", wants: nil},
		},
	},
	{
		name: "cvv",
		variants: []genVariant{
			{text: "CVV 123", wants: []want{{"123", recognizer.CVV}}},
			{text: "код безопасности 456", wants: []want{{"456", recognizer.CVV}}},
			{text: "cvv 789", wants: []want{{"789", recognizer.CVV}}},
			{text: "123", wants: nil},
		},
	},
	{
		name: "fullname",
		variants: []genVariant{
			{text: "ФИО: Иванов Иван Иванович", wants: []want{{"Иванов Иван Иванович", recognizer.FullName}}},
			{text: "клиент Александр Пушкин", wants: []want{{"Александр Пушкин", recognizer.FullName}}},
			{text: "ФИО: И. И. Иванов", wants: []want{{"И. И. Иванов", recognizer.FullName}}},
			{text: "фамилия: Иванов", wants: []want{{"Иванов", recognizer.FullName}}},
			{text: "поэт Александр Пушкин", wants: nil},
		},
	},
	{
		name: "birthdate",
		variants: []genVariant{
			{text: "дата рождения: 12.03.1990", wants: []want{{"12.03.1990", recognizer.BirthDate}}},
			{text: "родился 12 марта 1990 года", wants: []want{{"12 марта 1990 года", recognizer.BirthDate}}},
			{text: "дата рождения 25.12.1985", wants: []want{{"25.12.1985", recognizer.BirthDate}}},
			{text: "12.03.1990", wants: nil},
		},
	},
	{
		name: "birthplace",
		variants: []genVariant{
			{text: "место рождения: г. Москва", wants: []want{{"г. Москва", recognizer.BirthPlace}}},
			{text: "родился в Москве", wants: []want{{"в Москве", recognizer.BirthPlace}}},
			{text: "место рождения: город Казань", wants: []want{{"город Казань", recognizer.BirthPlace}}},
			{text: "Москва", wants: nil},
		},
	},
	{
		name: "citizenship",
		variants: []genVariant{
			{text: "гражданство: Российская Федерация", wants: []want{{"Российская Федерация", recognizer.Citizenship}}},
			{text: "гражданин Казахстана", wants: []want{{"Казахстана", recognizer.Citizenship}}},
			{text: "гражданство РФ", wants: []want{{"РФ", recognizer.Citizenship}}},
			{text: "Российская Федерация", wants: nil},
		},
	},
	{
		name: "authority",
		variants: []genVariant{
			{text: "паспорт выдан ОУФМС России по г. Москве", wants: []want{{"ОУФМС России по г. Москве", recognizer.PassportAuthority}}},
			{text: "выдан Отделом УФМС России по г. Москве", wants: []want{{"Отделом УФМС России по г. Москве", recognizer.PassportAuthority}}},
			{text: "ОУФМС России по г. Москве", wants: nil},
		},
	},
	{
		name: "issuedate",
		variants: []genVariant{
			{text: "дата выдачи: 15.04.2015", wants: []want{{"15.04.2015", recognizer.PassportIssueDate}}},
			{text: "выдан 12.03.2015", wants: []want{{"12.03.2015", recognizer.PassportIssueDate}}},
			{text: "15.04.2015", wants: nil},
		},
	},
	{
		name: "address",
		variants: []genVariant{
			{text: "адрес клиента: г. Москва, ул. Ленина, д. 5, кв. 10", wants: []want{{"г. Москва, ул. Ленина, д. 5, кв. 10", recognizer.Address}}},
			{text: "проживает по адресу: г. Казань, ул. Баумана, д. 12", wants: []want{{"г. Казань, ул. Баумана, д. 12", recognizer.Address}}},
			{text: "адрес отделения: г. Москва, ул. Тверская, д. 1", wants: nil},
			{text: "г. Москва, ул. Ленина, д. 5", wants: nil},
		},
	},
	{
		name: "cardholder",
		variants: []genVariant{
			{text: "держатель карты IVAN IVANOV", wants: []want{{"IVAN IVANOV", recognizer.CardHolderName}}},
			{text: "имя на карте Иван Иванов", wants: []want{{"Иван Иванов", recognizer.CardHolderName}}},
			{text: "card holder Ivan Ivanov", wants: []want{{"Ivan Ivanov", recognizer.CardHolderName}}},
			{text: "IVAN IVANOV", wants: nil},
		},
	},
}

// allExamples returns the full dataset: curated dev, curated held-out and the
// generated variants. The generated variants are split so every category is
// represented in both the dev and held-out halves.
func allExamples() []Example {
	dev := devSet()
	held := heldOutSet()
	gen := generateVariants()
	// Generated variants are already split: first half is dev, second half is
	// held-out. Append them accordingly.
	half := len(gen) / 2
	dev = append(dev, gen[:half]...)
	held = append(held, gen[half:]...)
	return append(dev, held...)
}

// splitExamples returns the dev and held-out sets separately.
func splitExamples() (dev, held []Example) {
	dev = devSet()
	held = heldOutSet()
	gen := generateVariants()
	half := len(gen) / 2
	dev = append(dev, gen[:half]...)
	held = append(held, gen[half:]...)
	return dev, held
}

// SplitExamples returns the dev and held-out sets separately. It is the
// exported entry point used by the evaluation command and tests.
func SplitExamples() (dev, held []Example) {
	return splitExamples()
}

// normalizeCase is a helper used by tests to build case variants.
func normalizeCase(s string) string { return strings.ToLower(s) }
