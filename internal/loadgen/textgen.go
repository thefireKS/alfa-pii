// Package loadgen provides a reproducible load generator for the /process
// endpoint. It pre-generates synthetic texts from different data categories
// with a fixed seed so the cost of generating inputs is never attributed to
// the service under test. The generator drives pairs of mask/restore requests
// bound by a unique payload_id, applies retries with Retry-After, classifies
// responses for compatibility checks and reports target vs actual throughput.
package loadgen

import (
	"fmt"
	"math/rand"
	"strings"
)

// DefaultSeed is the fixed seed used to reproduce a load profile. Re-running
// the generator with the same seed produces the same synthetic texts and the
// same pair sequence.
const DefaultSeed = 20260922

// Category is a synthetic data category used to build a payload.
type Category string

// Supported synthetic categories. Each produces a recognizable fragment so the
// mask differs from the original and restoration can be verified.
const (
	CatFullName  Category = "full_name"
	CatPhone     Category = "phone"
	CatEmail     Category = "email"
	CatPassport  Category = "passport"
	CatCard      Category = "card"
	CatAddress   Category = "address"
	CatBirthDate Category = "birth_date"
	CatINN       Category = "inn"
)

// AllCategories lists every supported category.
var AllCategories = []Category{
	CatFullName, CatPhone, CatEmail, CatPassport, CatCard, CatAddress, CatBirthDate, CatINN,
}

// SizeProfile describes the distribution of payload sizes in characters.
type SizeProfile struct {
	// Small is the fraction of payloads in the small bucket.
	Small float64
	// Medium is the fraction in the medium bucket.
	Medium float64
	// Large is the fraction in the large bucket.
	Large float64
}

// DefaultSizeProfile returns a realistic mix dominated by small texts with a
// tail of larger ones.
func DefaultSizeProfile() SizeProfile {
	return SizeProfile{Small: 0.7, Medium: 0.25, Large: 0.05}
}

// TextGenerator produces synthetic payloads from a fixed seed. It is
// deterministic: the same seed yields the same sequence of texts. Generation
// happens up front, outside the measured window, so the generator cost is not
// attributed to the service.
type TextGenerator struct {
	rng    *rand.Rand
	cat    []Category
	small  int
	medium int
	large  int
	// filler is a pool of neutral words used to pad texts to a target size.
	filler []string
}

// NewTextGenerator builds a generator with the given seed and size profile.
// small, medium and large are the target character lengths of each bucket.
func NewTextGenerator(seed int64, profile SizeProfile, small, medium, large int) *TextGenerator {
	return &TextGenerator{
		rng:    rand.New(rand.NewSource(seed)),
		cat:    AllCategories,
		small:  small,
		medium: medium,
		large:  large,
		filler: []string{
			"клиент", "заявка", "обработка", "данные", "запись", "система",
			"операция", "документ", "запрос", "ответ", "проверка", "статус",
		},
	}
}

// Next returns the next synthetic payload and the category it is built from.
// The text always contains at least one recognizable fragment so the mask
// differs from the original.
func (g *TextGenerator) Next() (string, Category) {
	cat := g.cat[g.rng.Intn(len(g.cat))]
	base := g.fragment(cat)
	target := g.targetLen()
	text := g.pad(base, target)
	return text, cat
}

// targetLen picks a bucket length according to the size profile.
func (g *TextGenerator) targetLen() int {
	r := g.rng.Float64()
	switch {
	case r < 0.7:
		return g.small
	case r < 0.95:
		return g.medium
	default:
		return g.large
	}
}

// fragment returns a synthetic sensitive fragment for the category.
func (g *TextGenerator) fragment(cat Category) string {
	switch cat {
	case CatFullName:
		return fmt.Sprintf("ФИО: %s %s %s", g.surname(), g.given(), g.patronymic())
	case CatPhone:
		return fmt.Sprintf("телефон +7 %03d %03d-%02d-%02d", g.rng.Intn(1000), g.rng.Intn(1000), g.rng.Intn(100), g.rng.Intn(100))
	case CatEmail:
		return fmt.Sprintf("email %s.%s@example.ru", g.latin(6), g.latin(6))
	case CatPassport:
		return fmt.Sprintf("паспорт %04d %06d", g.rng.Intn(10000), g.rng.Intn(1000000))
	case CatCard:
		return fmt.Sprintf("карта %04d %04d %04d %04d", g.rng.Intn(10000), g.rng.Intn(10000), g.rng.Intn(10000), g.rng.Intn(10000))
	case CatAddress:
		return fmt.Sprintf("адрес клиента: г. %s, ул. %s, д. %d, кв. %d", g.city(), g.street(), g.rng.Intn(200)+1, g.rng.Intn(300)+1)
	case CatBirthDate:
		return fmt.Sprintf("дата рождения: %02d.%02d.%04d", g.rng.Intn(28)+1, g.rng.Intn(12)+1, g.rng.Intn(60)+1950)
	case CatINN:
		return fmt.Sprintf("ИНН %010d", g.rng.Intn(10000000000))
	default:
		return "данные"
	}
}

// pad grows the text to the target length by appending neutral filler words so
// the payload reaches the desired size without changing the sensitive fragment.
func (g *TextGenerator) pad(base string, target int) string {
	if len(base) >= target {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	for b.Len() < target {
		b.WriteByte(' ')
		b.WriteString(g.filler[g.rng.Intn(len(g.filler))])
	}
	return b.String()
}

// FillerWord returns a deterministic neutral filler word from the generator's
// sequence. It is used to pad large texts to a target size.
func (g *TextGenerator) FillerWord() string {
	return g.filler[g.rng.Intn(len(g.filler))]
}

func (g *TextGenerator) surname() string {
	names := []string{"Иванов", "Петров", "Сидоров", "Кузнецов", "Смирнов", "Волков"}
	return names[g.rng.Intn(len(names))]
}

func (g *TextGenerator) given() string {
	names := []string{"Иван", "Пётр", "Алексей", "Дмитрий", "Сергей", "Николай"}
	return names[g.rng.Intn(len(names))]
}

func (g *TextGenerator) patronymic() string {
	names := []string{"Иванович", "Петрович", "Алексеевич", "Дмитриевич", "Сергеевич"}
	return names[g.rng.Intn(len(names))]
}

func (g *TextGenerator) city() string {
	cities := []string{"Москва", "Казань", "Новосибирск", "Екатеринбург", "Самара"}
	return cities[g.rng.Intn(len(cities))]
}

func (g *TextGenerator) street() string {
	streets := []string{"Ленина", "Пушкина", "Гагарина", "Советская", "Мира"}
	return streets[g.rng.Intn(len(streets))]
}

func (g *TextGenerator) latin(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(letters[g.rng.Intn(len(letters))])
	}
	return b.String()
}