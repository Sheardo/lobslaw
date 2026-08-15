package types

// TrustTier classifies an LLM provider's data-handling posture.
type TrustTier string

// The trust tiers, most to least trusted. A role's configured floor
// is compared against these with AtLeast, so the ordering below is
// load-bearing — see rank.
const (
	// TrustLocal is inference on hardware the operator controls;
	// prompt content never leaves the host.
	TrustLocal TrustTier = "local"
	// TrustPrivate is a third-party provider under a contract that
	// excludes training on submitted data.
	TrustPrivate TrustTier = "private"
	// TrustPublic is any provider with no such guarantee. Roles that
	// see sensitive context should set a floor above this.
	TrustPublic TrustTier = "public"
)

// IsValid reports whether t is one of the three known tiers, so a
// typo in a provider's configured trust fails at boot rather than
// silently ranking as zero and failing every AtLeast check.
func (t TrustTier) IsValid() bool {
	switch t {
	case TrustLocal, TrustPrivate, TrustPublic:
		return true
	}
	return false
}

// AtLeast reports whether t satisfies a floor set by other.
// Ordering: local > private > public.
func (t TrustTier) AtLeast(other TrustTier) bool {
	return t.rank() >= other.rank()
}

func (t TrustTier) rank() int {
	switch t {
	case TrustLocal:
		return 3
	case TrustPrivate:
		return 2
	case TrustPublic:
		return 1
	}
	return 0
}
