package types

// Effect is what the policy engine returns. EffectRequireConfirmation
// blocks until a human answers via ChannelService.Prompt; timeout
// and denial both fail closed.
type Effect string

// The three effects a policy rule can produce. These are the wire
// values used in policy TOML and in the audit log, so they are part
// of the operator-facing contract and must not be renamed.
const (
	// EffectAllow permits the invocation with no further checks.
	EffectAllow Effect = "allow"
	// EffectDeny refuses the invocation outright.
	EffectDeny Effect = "deny"
	// EffectRequireConfirmation defers to a human before proceeding.
	EffectRequireConfirmation Effect = "require_confirmation"
)

// IsValid reports whether e is one of the three known effects.
// Config loaders call this so an unrecognised effect in policy TOML
// fails at boot rather than being treated as a silent deny.
func (e Effect) IsValid() bool {
	switch e {
	case EffectAllow, EffectDeny, EffectRequireConfirmation:
		return true
	}
	return false
}
