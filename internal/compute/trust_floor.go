package compute

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The trust floor, enforced on the paths content actually leaves by.
//
// `min_trust_tier` in SOUL.md was parsed, validated, logged at boot and
// rendered into the system prompt — and enforced nowhere. The only code
// that checked it was Resolver.buildDecision, and nothing calls
// Resolver.Resolve: the turn path is the provider BACKUP chain, which
// walked from label to label with no notion of a tier. soul's own
// ValidateProviderTier had no callers either.
//
// So an operator could set min_trust_tier = "private", watch it appear
// in the prompt, and have a turn silently complete on a public provider
// the moment the primary returned a 429. The failover machinery that
// makes the assistant resilient was the same machinery that quietly
// lowered the floor.
//
// A floor checked at the first candidate and nowhere after is not a
// floor. It is a floor with a hole in it exactly where the interesting
// case lives, because the whole point of a backup is that it is used
// when something has gone wrong.

// ErrBelowTrustFloor is returned when nothing available meets the
// configured floor.
//
// Its own error, not a generic "all providers failed": the two send an
// operator to completely different places. One is an outage; this is a
// configuration that cannot serve a turn, and no amount of waiting
// fixes it.
type ErrBelowTrustFloor struct {
	Floor      types.TrustTier
	Considered []TrustCandidate
}

func (e *ErrBelowTrustFloor) Error() string {
	if len(e.Considered) == 0 {
		return fmt.Sprintf("no provider is configured at or above the trust floor %q", e.Floor)
	}
	parts := make([]string, 0, len(e.Considered))
	for _, c := range e.Considered {
		parts = append(parts, fmt.Sprintf("%s(%s)", c.Label, c.Tier))
	}
	sort.Strings(parts)
	// Every candidate is named with its tier, because "nothing meets
	// the floor" without the list leaves an operator diffing their own
	// config against a number they cannot see.
	return fmt.Sprintf(
		"no provider meets the trust floor %q; considered %s — raise a provider's trust_tier "+
			"or lower min_trust_tier in SOUL.md",
		e.Floor, strings.Join(parts, ", "))
}

// TrustCandidate is a provider considered for a turn.
type TrustCandidate struct {
	Label string
	Tier  types.TrustTier
}

// MeetsFloor reports whether a candidate may carry content under this
// floor.
//
// An unset floor permits everything: an operator who has not opted in
// has not asked for a restriction, and inventing one would break every
// existing deployment on upgrade.
//
// An INVALID floor permits nothing. A typo is not an opt-out — the
// operator asked for a restriction and got a string the code does not
// recognise, and treating that as "no floor" would silently grant the
// opposite of what they wrote. Config validation catches this at boot;
// this is the second line.
//
// A candidate with an invalid or empty tier fails any set floor, for
// the same reason: an undeclared tier is not evidence of a high one.
func MeetsFloor(floor types.TrustTier, tier types.TrustTier) bool {
	if floor == "" {
		return true
	}
	if !floor.IsValid() {
		return false
	}
	if !tier.IsValid() {
		return false
	}
	return tier.AtLeast(floor)
}

// FloorOf reads the floor from a soul accessor, tolerating nil at
// every level.
//
// A function rather than a value because the soul is tunable at
// runtime: reading it once at construction would pin the floor to
// whatever it was at boot, and an operator raising it would find the
// change took effect in the prompt and not in the routing.
func FloorOf(soul func() *types.SoulConfig) types.TrustTier {
	if soul == nil {
		return ""
	}
	s := soul()
	if s == nil {
		return ""
	}
	return s.MinTrustTier
}
