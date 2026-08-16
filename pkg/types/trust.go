package types

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// TrustTier is how exposed a provider leaves the content it handles,
// on a scale where HIGHER IS MORE TRUSTED.
//
// A number rather than an enum of three, because three was never the
// real shape. A model running on a VPS the operator rents is not
// `local` — the hardware is somebody else's — and it is plainly better
// than a public API with no contract. With a closed enum that provider
// has to be filed under a tier that misdescribes it, and the floor
// then admits or excludes it for the wrong reason.
//
// The named tiers stay, as reserved points on the scale. They carry
// something a bare number cannot: `local` is a categorical claim about
// where the hardware is, not a position on a continuum. Config may
// write either form.
type TrustTier int

const (
	// TrustUnset is the zero value, and is NOT a tier.
	//
	// Reserved deliberately. An omitted field decodes to Go's zero, so
	// if the zero value were a real tier every provider that forgot to
	// declare one would silently acquire it — which is exactly the bug
	// SkillTier hit when TierAgent was the zero value and every
	// struct-literal skill was quietly demoted. Here the cost would be
	// worse than a demotion: an undeclared provider would compare as a
	// genuine tier against a floor.
	TrustUnset TrustTier = 0

	// TrustPublic is any provider with no contractual guarantee about
	// submitted data.
	//
	// 1 rather than 0 for the reason above, and it is the one place
	// this scale departs from round numbers. Letting public be the zero
	// value would make "nobody declared a tier" and "somebody declared
	// the weakest tier" the same value, and those are different facts.
	TrustPublic TrustTier = 1

	// TrustPrivate is a third party under a contract that excludes
	// training on submitted data.
	TrustPrivate TrustTier = 50

	// TrustLocal is inference on hardware the operator controls;
	// content never leaves the host.
	TrustLocal TrustTier = 100

	// MaxTrustTier bounds the scale. A ceiling exists so that "more
	// trusted than local" is not expressible: there is nothing beyond
	// content never leaving the machine, and a config claiming 500
	// would be asserting something the tier system cannot mean.
	MaxTrustTier TrustTier = 100
)

// trustNames maps the reserved points to their names. The three are
// immovable: a deployment that pinned a provider at "private" and a
// floor at 50 must keep meaning the same thing after any future
// change, so these values can be added to but never renumbered.
var trustNames = map[TrustTier]string{
	TrustPublic:  "public",
	TrustPrivate: "private",
	TrustLocal:   "local",
}

// ParseTrustTier reads a name or a number.
//
// Both forms, because the names are what almost every config wants and
// the numbers are what the awkward cases need. An unrecognised name is
// an error rather than a new tier — operator-defined tier names would
// turn a typo into a silent extra tier, which is the one failure mode
// this scale must not have.
func ParseTrustTier(s string) (TrustTier, error) {
	trimmed := strings.ToLower(strings.TrimSpace(s))
	if trimmed == "" {
		return TrustUnset, nil
	}
	for tier, name := range trustNames {
		if trimmed == name {
			return tier, nil
		}
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return TrustUnset, fmt.Errorf(
			"trust tier %q is neither a known tier (public, private, local) nor a number", s)
	}
	return validTier(n)
}

// validTier range-checks a raw number, with an error that says which
// way the scale runs. Somebody who wrote 0 meaning "most trusted" has
// the direction backwards, and the message is the only chance to say
// so before they conclude the floor is broken.
func validTier(n int) (TrustTier, error) {
	tier := TrustTier(n)
	if !tier.IsValid() {
		return TrustUnset, fmt.Errorf(
			"trust tier %d is out of range; use 1..%d, where higher is more trusted",
			n, int(MaxTrustTier))
	}
	return tier, nil
}

// IsValid reports whether t is a usable tier.
//
// Zero is excluded on purpose: it means nobody said. A floor compared
// against an unset tier must fail, and IsValid is what every caller
// checks before trusting the comparison.
func (t TrustTier) IsValid() bool {
	return t >= TrustPublic && t <= MaxTrustTier
}

// AtLeast reports whether t satisfies a floor set by other.
//
// Plain >=, now that the scale is ordered by construction. The old
// rank() switch existed only to impose an order on three strings, and
// a comparison that needs a lookup table is one that can disagree with
// itself the moment somebody adds a value and forgets the table.
func (t TrustTier) AtLeast(other TrustTier) bool { return t >= other }

// String renders the reserved name when t sits exactly on one, and the
// number otherwise.
//
// Named where a name exists because that is what the operator wrote
// and what they will search their config for. A bare 50 in a log line
// sends somebody to a scale definition; "private" does not.
func (t TrustTier) String() string {
	if name, ok := trustNames[t]; ok {
		return name
	}
	if t == TrustUnset {
		return "unset"
	}
	return strconv.Itoa(int(t))
}

// MarshalText renders the same form String does, so a config
// round-trips as the operator wrote it wherever possible.
func (t TrustTier) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText accepts a name or a number. Used by koanf for TOML and
// by anything else going through encoding.TextUnmarshaler.
//
// Note that a BARE TOML integer never reaches here — mapstructure
// converts int to int directly — so config.Validate range-checks that
// path separately.
func (t *TrustTier) UnmarshalText(b []byte) error {
	parsed, err := ParseTrustTier(string(b))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// UnmarshalYAML accepts a name or a number in SOUL.md frontmatter.
//
// Separate from UnmarshalText because yaml.v3 does not consult
// encoding.TextUnmarshaler — a type that decodes correctly from TOML
// and silently zeroes from YAML is precisely the asymmetry that would
// leave a soul floor unset while the config looked right.
func (t *TrustTier) UnmarshalYAML(unmarshal func(any) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		parsed, err := validTier(n)
		if err != nil {
			return err
		}
		*t = parsed
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("trust tier must be a name or a number: %w", err)
	}
	return t.UnmarshalText([]byte(s))
}

// UnmarshalJSON accepts a name or a number.
//
// encoding/json consults TextUnmarshaler for map KEYS only, never for
// values, so this cannot be inherited from UnmarshalText.
func (t *TrustTier) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		parsed, err := validTier(n)
		if err != nil {
			return err
		}
		*t = parsed
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("trust tier must be a name or a number: %w", err)
	}
	return t.UnmarshalText([]byte(s))
}

// MarshalJSON emits the same form String does.
func (t TrustTier) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }
