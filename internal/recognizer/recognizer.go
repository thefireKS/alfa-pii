// Package recognizer finds sensitive fragments in text. Each recognizer
// reports the data type and the byte range (UTF-8) of every fragment it
// finds. Byte offsets are the single unit of measure shared by all
// recognizers and the replacement mechanism.
package recognizer

import (
	"errors"
	"sort"
	"unicode/utf8"
)

// Type identifies a category of sensitive data.
type Type string

// Supported data types.
const (
	Email             Type = "email"
	Phone             Type = "phone"
	INN               Type = "inn"
	Card              Type = "card"
	Passport          Type = "passport"
	DepartmentCode    Type = "department_code"
	DriverLicense     Type = "driver_license"
	PIN               Type = "pin"
	CVV               Type = "cvv"
	FullName          Type = "full_name"
	BirthDate         Type = "birth_date"
	BirthPlace        Type = "birth_place"
	Citizenship       Type = "citizenship"
	PassportAuthority Type = "passport_authority"
	PassportIssueDate Type = "passport_issue_date"
	Address           Type = "address"
	CardHolderName    Type = "card_holder_name"
)

// Fragment is a single sensitive occurrence in a text.
type Fragment struct {
	Type Type
	// Start and End are byte offsets into the original UTF-8 text. End is
	// exclusive.
	Start int
	End   int
}

// Recognizer finds fragments of a data type in a text.
type Recognizer interface {
	// Type returns the data type this recognizer detects.
	Type() Type
	// Find returns all fragments of the type found in text. A recognizer
	// returns an error only for an internal failure; a text with no matches
	// yields an empty slice and a nil error.
	Find(text string) ([]Fragment, error)
}

// ErrProtection is returned when a recognizer reports an internally invalid
// range. The operation fails closed so no partially masked text is produced.
var ErrProtection = errors.New("invalid fragment range")

// Priority returns the conflict-resolution priority of a type. Higher values
// win when two fragments partially overlap. It is exported so evaluation and
// other consumers can reproduce the resolution semantics.
func Priority(t Type) int {
	return typePriority[t]
}

// typePriority orders types for deterministic conflict resolution. When two
// fragments partially overlap, the higher-priority type wins; the overlap is
// never resolved by extending a range to cover the whole string.
var typePriority = map[Type]int{
	Email:             100,
	Card:              90,
	CardHolderName:    85,
	Passport:          80,
	DriverLicense:     80,
	Phone:             70,
	INN:               60,
	BirthDate:         55,
	BirthPlace:        55,
	Citizenship:       55,
	PassportAuthority: 55,
	PassportIssueDate: 55,
	Address:           55,
	DepartmentCode:    50,
	FullName:          45,
	PIN:               40,
	CVV:               30,
}

// Resolve validates all fragments and resolves overlaps deterministically
// using the built-in type priorities.
func Resolve(text string, frags []Fragment) ([]Fragment, error) {
	return ResolveWithPriority(text, frags, Priority)
}

// ResolveWithPriority validates all fragments and resolves overlaps
// deterministically. Identical and nested ranges collapse to a single range so
// no byte is replaced twice. A partial overlap keeps the higher-priority type
// instead of extending to the whole string. An invalid range (out of bounds,
// inverted, or not on a rune boundary) returns ErrProtection. The priority
// function orders types for conflict resolution; it is provided so
// config-driven types can participate with their configured priority.
func ResolveWithPriority(text string, frags []Fragment, priority func(Type) int) ([]Fragment, error) {
	n := len(text)
	rs := make([]Fragment, 0, len(frags))
	for _, f := range frags {
		if f.Start < 0 || f.End > n || f.Start > f.End {
			return nil, ErrProtection
		}
		if !utf8.ValidString(text[f.Start:f.End]) {
			return nil, ErrProtection
		}
		rs = append(rs, f)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Start != rs[j].Start {
			return rs[i].Start < rs[j].Start
		}
		if rs[i].End != rs[j].End {
			return rs[i].End > rs[j].End
		}
		if priority(rs[i].Type) != priority(rs[j].Type) {
			return priority(rs[i].Type) > priority(rs[j].Type)
		}
		return rs[i].Type < rs[j].Type
	})
	out := rs[:0]
	for _, f := range rs {
		if len(out) == 0 {
			out = append(out, f)
			continue
		}
		last := &out[len(out)-1]
		if f.Start >= last.End {
			out = append(out, f)
			continue
		}
		if f.End <= last.End {
			// Identical or nested within last: keep the outer range.
			continue
		}
		if f.Start == last.Start {
			// Same start, f is longer: keep f.
			last.Type = f.Type
			last.End = f.End
			continue
		}
		// Partial overlap: cover all sensitive bytes by taking the union of
		// the two ranges, keeping the higher-priority type. This never extends
		// to the whole string, only to the union of the overlapping fragments,
		// so no sensitive byte of the lower-priority fragment is left open.
		if priority(f.Type) > priority(last.Type) {
			last.Type = f.Type
		}
		last.End = f.End
	}
	return out, nil
}
