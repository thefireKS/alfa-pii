package recognizer

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrUnknownType is returned when a configured type has no recognizer.
var ErrUnknownType = errors.New("unknown data type")

// ErrInvalidRule is returned when a config-driven regexp rule is invalid.
var ErrInvalidRule = errors.New("invalid regexp rule")

// RegexpRule is a config-driven recognition rule. It lets a new data type be
// added without new Go code: the pattern is compiled at startup, the type and
// priority are configured, and the number of matches per text is bounded.
type RegexpRule struct {
	// DataType is the data type this rule detects.
	DataType Type
	// Priority orders this type in conflict resolution.
	Priority int
	// Pattern is a standard Go regular expression.
	Pattern string
	// MaxMatches bounds the number of fragments produced per text.
	MaxMatches int

	re *regexp.Regexp
}

// Registry maps data types to recognizer factories and holds config-driven
// regexp rules. New types are added through the registry and configuration
// without rewriting the core.
type Registry struct {
	factories map[Type]func() Recognizer
	rules     []*RegexpRule
}

// NewRegistry returns a Registry preloaded with the built-in recognizers.
func NewRegistry() *Registry {
	r := &Registry{factories: make(map[Type]func() Recognizer)}
	r.factories[Email] = func() Recognizer { return EmailRecognizer{} }
	r.factories[Phone] = func() Recognizer { return PhoneRecognizer{} }
	r.factories[INN] = func() Recognizer { return INNRecognizer{} }
	r.factories[Card] = func() Recognizer { return CardRecognizer{} }
	r.factories[Passport] = func() Recognizer { return PassportRecognizer{} }
	r.factories[DepartmentCode] = func() Recognizer { return DepartmentCodeRecognizer{} }
	r.factories[DriverLicense] = func() Recognizer { return DriverLicenseRecognizer{} }
	r.factories[PIN] = func() Recognizer { return PINRecognizer{} }
	r.factories[CVV] = func() Recognizer { return CVVRecognizer{} }
	r.factories[FullName] = func() Recognizer { return FullNameRecognizer{} }
	r.factories[BirthDate] = func() Recognizer { return BirthDateRecognizer{} }
	r.factories[BirthPlace] = func() Recognizer { return BirthPlaceRecognizer{} }
	r.factories[Citizenship] = func() Recognizer { return CitizenshipRecognizer{} }
	r.factories[PassportAuthority] = func() Recognizer { return PassportAuthorityRecognizer{} }
	r.factories[PassportIssueDate] = func() Recognizer { return PassportIssueDateRecognizer{} }
	r.factories[Address] = func() Recognizer { return AddressRecognizer{} }
	r.factories[CardHolderName] = func() Recognizer { return CardHolderNameRecognizer{} }
	return r
}

// AddRegexpRule compiles and registers a config-driven rule. The pattern must
// compile and the type must not collide with a built-in type. MaxMatches must
// be positive. The rule is validated before it is registered so an invalid
// configuration never activates partially.
func (r *Registry) AddRegexpRule(typ Type, priority int, pattern string, maxMatches int) error {
	if typ == "" {
		return fmt.Errorf("%w: empty type", ErrInvalidRule)
	}
	if _, ok := r.factories[typ]; ok {
		return fmt.Errorf("%w: type %q is a built-in type", ErrInvalidRule, typ)
	}
	if maxMatches <= 0 {
		return fmt.Errorf("%w: type %q: max matches must be positive", ErrInvalidRule, typ)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%w: type %q: %v", ErrInvalidRule, typ, err)
	}
	r.rules = append(r.rules, &RegexpRule{DataType: typ, Priority: priority, Pattern: pattern, MaxMatches: maxMatches, re: re})
	return nil
}

// Known reports whether a type is recognized by the registry (built-in or a
// configured regexp rule).
func (r *Registry) Known(t Type) bool {
	if _, ok := r.factories[t]; ok {
		return true
	}
	for _, rule := range r.rules {
		if rule.DataType == t {
			return true
		}
	}
	return false
}

// Priority returns the conflict-resolution priority of a type, preferring a
// configured regexp rule and falling back to the built-in priority.
func (r *Registry) Priority(t Type) int {
	for _, rule := range r.rules {
		if rule.DataType == t {
			return rule.Priority
		}
	}
	return Priority(t)
}

// Recognizers returns the recognizers for the given types. Every type must be
// known to the registry; otherwise ErrUnknownType is returned and no partial
// set is produced. The returned priority function resolves conflicts using the
// registry's priorities.
func (r *Registry) Recognizers(types []Type) ([]Recognizer, func(Type) int, error) {
	seen := make(map[Type]bool)
	var recs []Recognizer
	for _, t := range types {
		if seen[t] {
			continue
		}
		seen[t] = true
		if f, ok := r.factories[t]; ok {
			recs = append(recs, f())
			continue
		}
		for _, rule := range r.rules {
			if rule.DataType == t {
				recs = append(recs, rule)
				break
			}
		}
		if !r.Known(t) {
			return nil, nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
		}
	}
	return recs, r.Priority, nil
}

// Type implements Recognizer for a regexp rule.
func (r *RegexpRule) Type() Type { return r.DataType }

// Find implements Recognizer. It returns at most MaxMatches fragments so a
// pathological text cannot produce an unbounded result.
func (r *RegexpRule) Find(text string) ([]Fragment, error) {
	idx := r.re.FindAllStringIndex(text, -1)
	if len(idx) > r.MaxMatches {
		idx = idx[:r.MaxMatches]
	}
	frags := make([]Fragment, 0, len(idx))
	for _, m := range idx {
		frags = append(frags, Fragment{Type: r.DataType, Start: m[0], End: m[1]})
	}
	return frags, nil
}
