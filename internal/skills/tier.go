package skills

// Precedence used to be version-first: a higher semver won, and
// signing was only a tie-break at equal version — and only under
// SigningPrefer at that.
//
// That was defensible while nothing but an operator could write a
// skill. It stopped being defensible the moment the agent could author
// one, because the attack is a single line of YAML: name your skill
// after a signed one, set version 99.0.0, and it wins. Anything that
// can propose an artefact can then take over any name in the library.
//
// So precedence is tier-first. A higher version cannot promote a skill
// past its provenance.

// SkillTier is where a skill came from, which now decides who wins a
// contested name.
type SkillTier int

const (
	// tierUnset is the ZERO VALUE on purpose, so a Skill built as a
	// struct literal derives its tier from how it arrived rather than
	// silently defaulting to the lowest one. Making TierAgent the zero
	// value looked tidier and demoted every hand-constructed skill to
	// the bottom of the order.
	tierUnset SkillTier = iota

	// TierAgent is written by the review fork. Lowest of the real
	// tiers, deliberately: it is the only one whose author is not a
	// person.
	TierAgent
	// TierOperator is on-disk, operator-authored.
	TierOperator
	// TierSigned is a bundle whose manifest verified against a trusted
	// publisher key.
	TierSigned
)

func (t SkillTier) String() string {
	switch t {
	case TierSigned:
		return "signed"
	case TierOperator:
		return "operator"
	case TierAgent:
		return "agent"
	default:
		return "underived"
	}
}

// tierOf derives a skill's tier from how it arrived.
//
// Agent is never derived — it is set explicitly by whatever
// materialises the self-taught store, because provenance-by-location
// is what establishes it and a parsed manifest carries no trace of
// having been machine-written. Anything reaching Parse came off a
// disk an operator controls.
func tierOf(s *Skill) SkillTier {
	if s.Tier != tierUnset {
		return s.Tier
	}
	if s.IsSigned {
		return TierSigned
	}
	return TierOperator
}
