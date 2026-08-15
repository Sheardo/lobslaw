package types

// RiskTier classifies how reversible a tool invocation is. Policy
// defaults to require_confirmation for irreversible and
// communicating-to-untrusted, allow for reversible.
type RiskTier string

// The risk tiers, ordered least to most consequential. These are the
// wire values used in tool manifests and policy TOML.
const (
	// RiskReversible covers actions whose effects can be undone —
	// reads, and writes inside a sandboxed workspace.
	RiskReversible RiskTier = "reversible"
	// RiskCommunicating covers actions that emit data to a third
	// party. Not undoable once sent, even though nothing is destroyed.
	RiskCommunicating RiskTier = "communicating"
	// RiskIrreversible covers destructive or externally-durable
	// actions — deletions, payments, published writes.
	RiskIrreversible RiskTier = "irreversible"
)

// IsValid reports whether r is one of the three known tiers, so an
// unrecognised risk in a tool manifest fails loudly at load time
// instead of defaulting into the permissive tier.
func (r RiskTier) IsValid() bool {
	switch r {
	case RiskReversible, RiskCommunicating, RiskIrreversible:
		return true
	}
	return false
}
