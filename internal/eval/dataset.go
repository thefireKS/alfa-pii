package eval

import (
	"sort"
	"strings"

	"alfa-hackathon.local/pii/internal/recognizer"
)

// want is a sensitive value expected in an example together with its type.
type want struct {
	value string
	typ   recognizer.Type
}

// ex builds an Example from a text and the expected sensitive values. Each
// value is located in the text and its byte range recorded, so offsets are
// always correct and in the same unit the service uses. Values must occur in
// the text; a missing value is a programming error and panics.
func ex(name, text string, wants ...want) Example {
	ranges := make([]Range, 0, len(wants))
	for _, w := range wants {
		idx := strings.Index(text, w.value)
		if idx < 0 {
			panic("eval: expected value not found in example " + name + ": " + w.value)
		}
		ranges = append(ranges, Range{Start: idx, End: idx + len(w.value), Type: w.typ})
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})
	return Example{Name: name, Text: text, Expected: ranges}
}

// devSet is the development dataset used to drive fixes. It covers all 17
// categories with positive, negative and mixed examples.
func devSet() []Example {
	return []Example{
		// --- Email ---
		ex("email_simple", "свяжитесь со мной по a.b@example.com пожалуйста",
			want{"a.b@example.com", recognizer.Email}),
		ex("email_tag", "почта user+tag@sub.domain.ru",
			want{"user+tag@sub.domain.ru", recognizer.Email}),
		ex("email_cyrillic_context", "напишите на почту ivanov@mail.ru",
			want{"ivanov@mail.ru", recognizer.Email}),
		ex("email_negative_no_dot", "адрес a@b не является почтой", nil...),
		ex("email_negative_plain", "здесь нет почты", nil...),

		// --- Phone ---
		ex("phone_plus_spaced", "тел +7 (912) 345-67-89",
			want{"+7 (912) 345-67-89", recognizer.Phone}),
		ex("phone_eight", "8 (912) 345-67-89",
			want{"8 (912) 345-67-89", recognizer.Phone}),
		ex("phone_compact_context", "мобильный 79123456789",
			want{"79123456789", recognizer.Phone}),
		ex("phone_negative_bare_seven", "79123456789", nil...),
		ex("phone_negative_order", "заказ 791234567890", nil...),
		ex("phone_negative_short", "номер 12345", nil...),

		// --- INN ---
		ex("inn_valid_10", "ИНН 7707083893",
			want{"7707083893", recognizer.INN}),
		ex("inn_valid_12", "ИНН 500100732259",
			want{"500100732259", recognizer.INN}),
		ex("inn_valid_bare", "7707083893",
			want{"7707083893", recognizer.INN}),
		ex("inn_invalid_signed", "ИНН 7707083894",
			want{"7707083894", recognizer.INN}),
		ex("inn_negative_invalid_bare", "7707083894", nil...),
		ex("inn_negative_order", "заказ 7707083893", nil...),

		// --- Card ---
		ex("card_spaced", "карта 4111 1111 1111 1111",
			want{"4111 1111 1111 1111", recognizer.Card}),
		ex("card_hyphen", "4111-1111-1111-1111",
			want{"4111-1111-1111-1111", recognizer.Card}),
		ex("card_compact", "4111111111111111",
			want{"4111111111111111", recognizer.Card}),
		ex("card_invalid_signed", "карта 4111111111111112",
			want{"4111111111111112", recognizer.Card}),
		ex("card_negative_invalid_bare", "4111111111111112", nil...),
		ex("card_negative_order", "заказ 4111111111111111", nil...),
		ex("card_after_amount_field", "Сумма: 100 рублей; карта: 4111 1111 1111 1111",
			want{"4111 1111 1111 1111", recognizer.Card}),

		// --- Passport ---
		ex("passport_spaced", "паспорт 4506 123456",
			want{"4506 123456", recognizer.Passport}),
		ex("passport_hyphen", "серия 4506-123456",
			want{"4506-123456", recognizer.Passport}),
		ex("passport_compact_signed", "паспорт 4506123456",
			want{"4506123456", recognizer.Passport}),
		ex("passport_negative_bare", "4506123456", nil...),
		ex("passport_negative_order", "заказ 4506123456", nil...),
		ex("passport_after_date_field", "Дата выдачи: 01.01.2020; паспорт 4510 123456",
			want{"01.01.2020", recognizer.PassportIssueDate},
			want{"4510 123456", recognizer.Passport}),

		// --- Department code ---
		ex("department_signed", "код подразделения 770-123",
			want{"770-123", recognizer.DepartmentCode}),
		ex("department_negative_bare", "770-123", nil...),
		ex("department_negative_no_hyphen", "код подразделения 770123", nil...),

		// --- Driver license ---
		ex("driver_spaced", "водительское удостоверение 7701 123456",
			want{"7701 123456", recognizer.DriverLicense}),
		ex("driver_compact_signed", "права 7701123456",
			want{"7701123456", recognizer.DriverLicense}),
		ex("driver_negative_bare", "7701123456", nil...),

		// --- PIN ---
		ex("pin_signed", "ПИН 1234",
			want{"1234", recognizer.PIN}),
		ex("pin_code", "пин-код 5678",
			want{"5678", recognizer.PIN}),
		ex("pin_negative_bare", "1234", nil...),
		ex("pin_negative_order", "заказ 1234", nil...),

		// --- CVV ---
		ex("cvv_signed", "CVV 123",
			want{"123", recognizer.CVV}),
		ex("cvv_russian", "код безопасности 456",
			want{"456", recognizer.CVV}),
		ex("cvv_negative_bare", "123", nil...),
		ex("cvv_after_date_field", "Дата: 01.01.2020; CVV: 123",
			want{"123", recognizer.CVV}),

		// --- Full name ---
		ex("fullname_labeled", "ФИО: Иванов Иван Иванович",
			want{"Иванов Иван Иванович", recognizer.FullName}),
		ex("fullname_client", "клиент Александр Пушкин",
			want{"Александр Пушкин", recognizer.FullName}),
		ex("fullname_initials", "ФИО: Иванов И. И.",
			want{"Иванов И. И.", recognizer.FullName}),
		ex("fullname_initials_first", "ФИО: И. И. Иванов",
			want{"И. И. Иванов", recognizer.FullName}),
		ex("fullname_surname_field", "фамилия: Иванов",
			want{"Иванов", recognizer.FullName}),
		ex("fullname_uppercase", "ФИО: ИВАНОВ ИВАН ИВАНОВИЧ",
			want{"ИВАНОВ ИВАН ИВАНОВИЧ", recognizer.FullName}),
		ex("fullname_lowercase", "ФИО: иванов иван иванович",
			want{"иванов иван иванович", recognizer.FullName}),
		ex("fullname_given_name_field", "Имя: Анна",
			want{"Анна", recognizer.FullName}),
		ex("fullname_client_own_name", "Клиент Александр Пушкин",
			want{"Александр Пушкин", recognizer.FullName}),
		ex("fullname_negative_poet", "поэт Александр Пушкин", nil...),
		ex("fullname_negative_bare", "Александр Пушкин", nil...),
		ex("fullname_negative_poet_reference", "Клиент прочитал стихи поэта Александр Пушкин", nil...),

		// --- Birth date ---
		ex("birthdate_numeric", "дата рождения: 12.03.1990",
			want{"12.03.1990", recognizer.BirthDate}),
		ex("birthdate_word", "дата рождения 12 марта 1990 года",
			want{"12 марта 1990 года", recognizer.BirthDate}),
		ex("birthdate_born", "родился 12.03.1990",
			want{"12.03.1990", recognizer.BirthDate}),
		ex("birthdate_uppercase_month", "Дата рождения: 12 МАРТА 1990 года",
			want{"12 МАРТА 1990 года", recognizer.BirthDate}),
		ex("birthdate_negative_bare", "12.03.1990", nil...),

		// --- Birth place ---
		ex("birthplace_city", "место рождения: г. Москва",
			want{"г. Москва", recognizer.BirthPlace}),
		ex("birthplace_born_city", "родился в г. Москве",
			want{"г. Москве", recognizer.BirthPlace}),
		ex("birthplace_born_bare", "родился в Москве",
			want{"в Москве", recognizer.BirthPlace}),
		ex("birthplace_bare_city", "Место рождения: Казань",
			want{"Казань", recognizer.BirthPlace}),
		ex("birthplace_negative_bare", "Москва", nil...),

		// --- Citizenship ---
		ex("citizenship_federation", "гражданство: Российская Федерация",
			want{"Российская Федерация", recognizer.Citizenship}),
		ex("citizenship_rf", "гражданин РФ",
			want{"РФ", recognizer.Citizenship}),
		ex("citizenship_country", "гражданин Казахстана",
			want{"Казахстана", recognizer.Citizenship}),
		ex("citizenship_negative_bare", "Российская Федерация", nil...),

		// --- Passport authority ---
		ex("authority_issued", "паспорт выдан ОУФМС России по г. Москве",
			want{"ОУФМС России по г. Москве", recognizer.PassportAuthority}),
		ex("authority_department", "выдан Отделом УФМС России по г. Москве",
			want{"Отделом УФМС России по г. Москве", recognizer.PassportAuthority}),
		ex("authority_negative_bare", "ОУФМС России по г. Москве", nil...),

		// --- Passport issue date ---
		ex("issuedate_labeled", "дата выдачи: 15.04.2015",
			want{"15.04.2015", recognizer.PassportIssueDate}),
		ex("issuedate_issued", "выдан 12.03.2015",
			want{"12.03.2015", recognizer.PassportIssueDate}),
		ex("issuedate_negative_bare", "15.04.2015", nil...),

		// --- Address ---
		ex("address_client", "адрес клиента: г. Москва, ул. Ленина, д. 5, кв. 10",
			want{"г. Москва, ул. Ленина, д. 5, кв. 10", recognizer.Address}),
		ex("address_residence", "проживает по адресу: г. Москва, ул. Ленина, д. 5",
			want{"г. Москва, ул. Ленина, д. 5", recognizer.Address}),
		ex("address_negative_branch", "адрес отделения: г. Москва, ул. Тверская, д. 1", nil...),
		ex("address_negative_bare", "г. Москва, ул. Ленина, д. 5", nil...),
		ex("address_branch_and_client", "адрес отделения: г. Москва, ул. Тверская, д. 1; адрес клиента: г. Москва, ул. Ленина, д. 5",
			want{"г. Москва, ул. Ленина, д. 5", recognizer.Address}),
		ex("address_composite", "Адрес проживания: г. Санкт-Петербург, ул. Большая Морская, д. 12А, кв. 7",
			want{"г. Санкт-Петербург, ул. Большая Морская, д. 12А, кв. 7", recognizer.Address}),

		// --- Card holder name ---
		ex("cardholder_latin", "держатель карты IVAN IVANOV",
			want{"IVAN IVANOV", recognizer.CardHolderName}),
		ex("cardholder_latin_title", "card holder Ivan Ivanov",
			want{"Ivan Ivanov", recognizer.CardHolderName}),
		ex("cardholder_cyrillic", "имя на карте Иван Иванов",
			want{"Иван Иванов", recognizer.CardHolderName}),
		ex("cardholder_negative_bare", "IVAN IVANOV", nil...),

		// --- Mixed sentences ---
		ex("mixed_passport_block",
			"паспорт 4506 123456 выдан ОУФМС России по г. Москве, дата выдачи 15.04.2015, код подразделения 770-123",
			want{"4506 123456", recognizer.Passport},
			want{"ОУФМС России по г. Москве", recognizer.PassportAuthority},
			want{"15.04.2015", recognizer.PassportIssueDate},
			want{"770-123", recognizer.DepartmentCode}),
		ex("mixed_client_block",
			"ФИО: Иванов Иван Иванович, дата рождения 12.03.1990, место рождения г. Москва, гражданство Российская Федерация",
			want{"Иванов Иван Иванович", recognizer.FullName},
			want{"12.03.1990", recognizer.BirthDate},
			want{"г. Москва", recognizer.BirthPlace},
			want{"Российская Федерация", recognizer.Citizenship}),
		ex("mixed_card_block",
			"держатель карты IVAN IVANOV, карта 4111 1111 1111 1111, CVV 123, ПИН 1234",
			want{"IVAN IVANOV", recognizer.CardHolderName},
			want{"4111 1111 1111 1111", recognizer.Card},
			want{"123", recognizer.CVV},
			want{"1234", recognizer.PIN}),
		ex("mixed_contact_block",
			"клиент Александр Пушкин, телефон +7 (912) 345-67-89, почта pushkin@example.com, адрес клиента: г. Москва, ул. Ленина, д. 5",
			want{"Александр Пушкин", recognizer.FullName},
			want{"+7 (912) 345-67-89", recognizer.Phone},
			want{"pushkin@example.com", recognizer.Email},
			want{"г. Москва, ул. Ленина, д. 5", recognizer.Address}),
		ex("mixed_negative_plain",
			"заказ 4506123456 на сумму 4111111111111111 рублей, дата 12.03.1990", nil...),
	}
}

// heldOutSet is the held-out dataset. Examples here are not used to drive
// fixes; when one is used to fix a bug it is moved to the regression tests and
// replaced by new independent variants.
func heldOutSet() []Example {
	return []Example{
		ex("held_email_plus", "почта: first.last+tag@example.co.uk",
			want{"first.last+tag@example.co.uk", recognizer.Email}),
		ex("held_phone_plus_compact", "тел.: +79123456789, спасибо",
			want{"+79123456789", recognizer.Phone}),
		ex("held_inn_12_bare", "500100732259",
			want{"500100732259", recognizer.INN}),
		ex("held_card_compact", "номер карты 4111111111111111",
			want{"4111111111111111", recognizer.Card}),
		ex("held_passport_compact", "паспорт 4506123456",
			want{"4506123456", recognizer.Passport}),
		ex("held_department", "код подразделения 123-456",
			want{"123-456", recognizer.DepartmentCode}),
		ex("held_driver", "водительское удостоверение 9901 654321",
			want{"9901 654321", recognizer.DriverLicense}),
		ex("held_pin", "ПИН-код 4321",
			want{"4321", recognizer.PIN}),
		ex("held_cvv", "код безопасности 789",
			want{"789", recognizer.CVV}),
		ex("held_fullname", "заявитель Петров Пётр Петрович",
			want{"Петров Пётр Петрович", recognizer.FullName}),
		ex("held_fullname_uppercase", "ФИО: СИДОРОВ СИДОР СИДОРОВИЧ",
			want{"СИДОРОВ СИДОР СИДОРОВИЧ", recognizer.FullName}),
		ex("held_fullname_lowercase", "ФИО: петров пётр петрович",
			want{"петров пётр петрович", recognizer.FullName}),
		ex("held_given_name_field", "Имя: Мария",
			want{"Мария", recognizer.FullName}),
		ex("held_client_own_name", "Клиент Сергей Есенин",
			want{"Сергей Есенин", recognizer.FullName}),
		ex("held_negative_poet_reference", "Клиент читал стихи поэта Сергей Есенин", nil...),
		ex("held_birthdate", "дата рождения 25 декабря 1985 года",
			want{"25 декабря 1985 года", recognizer.BirthDate}),
		ex("held_birthdate_uppercase_month", "Дата рождения: 5 ИЮНЯ 1988 года",
			want{"5 ИЮНЯ 1988 года", recognizer.BirthDate}),
		ex("held_birthplace", "место рождения: г. Санкт-Петербург",
			want{"г. Санкт-Петербург", recognizer.BirthPlace}),
		ex("held_birthplace_bare_city", "Место рождения: Тверь",
			want{"Тверь", recognizer.BirthPlace}),
		ex("held_birthplace_hyphen_alt", "родился в Ростове-на-Дону",
			want{"в Ростове-на-Дону", recognizer.BirthPlace}),
		ex("held_citizenship", "гражданство: Республика Беларусь",
			want{"Республика Беларусь", recognizer.Citizenship}),
		ex("held_citizenship_alt", "гражданин Республики Казахстан",
			want{"Республики Казахстан", recognizer.Citizenship}),
		ex("held_authority", "паспорт выдан УФМС России по г. Казани",
			want{"УФМС России по г. Казани", recognizer.PassportAuthority}),
		ex("held_issuedate", "дата выдачи 20.01.2010",
			want{"20.01.2010", recognizer.PassportIssueDate}),
		ex("held_passport_after_date_field", "Дата выдачи: 02.02.2021; паспорт 4511 654321",
			want{"02.02.2021", recognizer.PassportIssueDate},
			want{"4511 654321", recognizer.Passport}),
		ex("held_card_after_amount_field", "Сумма: 250 рублей; карта: 5555 5555 5555 4444",
			want{"5555 5555 5555 4444", recognizer.Card}),
		ex("held_cvv_after_date_field", "Дата: 03.03.2022; CVV: 456",
			want{"456", recognizer.CVV}),
		ex("held_address", "зарегистрирован по адресу: г. Казань, ул. Баумана, д. 12, кв. 3",
			want{"г. Казань, ул. Баумана, д. 12, кв. 3", recognizer.Address}),
		ex("held_address_composite", "Адрес регистрации: г. Нижний Новгород, ул. Большая Покровская, д. 3Б, кв. 12",
			want{"г. Нижний Новгород, ул. Большая Покровская, д. 3Б, кв. 12", recognizer.Address}),
		ex("held_cardholder", "имя на карте PETROV PETR",
			want{"PETROV PETR", recognizer.CardHolderName}),
		ex("held_negative_poet", "поэт Александр Пушкин написал роман в стихах", nil...),
		ex("held_negative_branch", "адрес банка: г. Москва, ул. Тверская, д. 1", nil...),
		ex("held_mixed", "клиент Сидоров Сидор Сидорович, телефон 8 903 123-45-67, ИНН 7707083893",
			want{"Сидоров Сидор Сидорович", recognizer.FullName},
			want{"8 903 123-45-67", recognizer.Phone},
			want{"7707083893", recognizer.INN}),
	}
}
