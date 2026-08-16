package node

import (
	"fmt"

	"github.com/jmylchreest/lobslaw/internal/soul"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The trust floor at boot.
//
// Enforcement at dispatch is the guarantee; this is the part that
// tells an operator BEFORE a turn fails. The two are not
// interchangeable — a floor discovered when the primary provider
// happens to 429 is discovered at the worst possible moment, and by
// somebody who is waiting for an answer rather than reading a config.

// trustFloorAccessor is what the agent and the builtins read per turn.
//
// A function, not a value: the soul is tunable at runtime, so reading
// the floor once at boot would pin it — and an operator raising it
// would find the change took effect in the system prompt and not in
// the routing, which is the most misleading half-application
// available.
func (n *Node) trustFloorAccessor() func() types.TrustTier {
	return func() types.TrustTier {
		s := n.Soul()
		if s == nil {
			return ""
		}
		return s.Config.MinTrustTier
	}
}

// validateTrustFloor checks the configured providers against the
// soul's floor at boot.
//
// Fatal when the PRIMARY is below the floor, warn for anything else.
// The asymmetry is the point: a backup below the floor is a config
// that degrades — the chain terminates earlier than the operator
// expects and the reason is logged — while a primary below the floor
// is a config where no turn can ever run. Booting into that and
// failing every request with "no provider meets the trust floor"
// wastes the one moment somebody was in a position to fix it.
func (n *Node) validateTrustFloor() error {
	floor := n.trustFloorAccessor()()
	if floor == "" {
		return nil
	}
	if !floor.IsValid() {
		// Refused rather than ignored. A typo is not an opt-out: the
		// operator asked for a restriction and wrote a string nothing
		// recognises, and treating that as "no floor" grants the exact
		// opposite of what they intended.
		return fmt.Errorf(
			"soul: min_trust_tier %q is not a recognised tier (local, private, public)", floor)
	}

	loaded := n.Soul()
	primary := n.primaryProviderLabel()
	var checked int
	for i := range n.cfg.Compute.Providers {
		p := n.cfg.Compute.Providers[i]
		checked++
		err := soul.ValidateProviderTier(loaded, soul.ProviderTrustTier{
			Label: p.Label, TrustTier: p.TrustTier,
		})
		if err == nil {
			continue
		}
		if p.Label == primary {
			return fmt.Errorf("%w — this is the primary provider, so no turn could run", err)
		}
		n.log.Warn("compute: provider is below the soul trust floor and will be skipped",
			"label", p.Label, "trust_tier", p.TrustTier, "min_trust_tier", floor)
	}

	n.log.Info("compute: trust floor enforced",
		"min_trust_tier", floor, "providers_checked", checked)
	return nil
}

// primaryProviderLabel is the provider the backup chain starts from.
//
// Extracted so boot validation and agent wiring cannot disagree about
// which provider is "the primary" — a check that validated a different
// provider from the one that runs is worse than no check, because it
// reports a clean bill of health for something it never looked at.
func (n *Node) primaryProviderLabel() string {
	if len(n.cfg.Compute.Providers) == 0 {
		return ""
	}
	if n.cfg.Compute.Roles.Main != "" {
		return n.cfg.Compute.Roles.Main
	}
	return n.cfg.Compute.Providers[0].Label
}
