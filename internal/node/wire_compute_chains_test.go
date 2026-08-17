package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Chains route now.
//
// They used to be parsed, validated for coherence, logged at boot — and
// inert: Resolver.Resolve had no callers, and the turn path went
// straight to ProviderRegistry.Chain(PrimaryLabel), which walks
// `backup` links and knows nothing about triggers or per-chain floors.
// This file used to assert the boot warning that admitted as much.
//
// What replaced it: the resolver picks WHERE THE BACKUP WALK BEGINS.
// Everything the walk already did — the floor at every candidate,
// health cooldowns, a span per attempt — is unchanged.

func chainNode(t *testing.T, chains []config.ChainConfig, defaultChain string) *Node {
	t.Helper()
	n := &Node{}
	n.cfg.Compute = config.ComputeConfig{
		Providers: []config.ProviderConfig{
			{Label: "cheap", Endpoint: "https://example.invalid", Model: "s", TrustTier: types.TrustPrivate},
			{Label: "big", Endpoint: "https://example.invalid", Model: "l", TrustTier: types.TrustPrivate},
		},
		Chains:       chains,
		DefaultChain: defaultChain,
	}
	r, err := compute.NewResolver(&n.cfg.Compute)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	n.resolver = r
	return n
}

func deepChain() config.ChainConfig {
	return config.ChainConfig{
		Label:   "deep",
		Trigger: config.ChainTriggerConfig{MinComplexity: 70},
		Steps:   []config.ChainStepConfig{{Provider: "big"}},
	}
}

// A hard turn reaches the chain configured for hard turns. Before this,
// it reached whatever roles.main pointed at, whatever the config said.
func TestAComplexTurnRoutesToItsChain(t *testing.T) {
	t.Parallel()
	n := chainNode(t, []config.ChainConfig{deepChain()}, "")
	got, err := n.resolver.Resolve(compute.ResolveRequest{Complexity: 85})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainLabel != "deep" {
		t.Errorf("chain = %q, want deep", got.ChainLabel)
	}
	if got.Steps[0].Provider.Label != "big" {
		t.Errorf("start = %q, want big", got.Steps[0].Provider.Label)
	}
}

// Below the trigger it must NOT fire, or a min_complexity threshold is
// decoration.
func TestASimpleTurnDoesNotTakeTheDeepChain(t *testing.T) {
	t.Parallel()
	n := chainNode(t, []config.ChainConfig{deepChain()}, "")
	got, err := n.resolver.Resolve(compute.ResolveRequest{Complexity: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainLabel == "deep" {
		t.Error("a complexity-20 turn took the deep chain")
	}
}

// The hint is sugar over chains: it selects the chain of the same
// LABEL, so an operator can read what `deep` means in their own config
// file and redefine it by editing that chain.
func TestAHintResolvesThroughAChainAnOperatorCanRead(t *testing.T) {
	t.Parallel()
	n := chainNode(t, []config.ChainConfig{deepChain()}, "")
	got, err := n.resolver.Resolve(compute.ResolveRequest{
		Complexity: 5, // low: nothing here triggers except the hint
		Hint:       compute.HintDeep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainLabel != "deep" {
		t.Errorf("chain = %q; the hint did not select its chain", got.ChainLabel)
	}
	if got.TriggerReason != "hint=deep" {
		t.Errorf("reason = %q; an operator cannot see the hint fired", got.TriggerReason)
	}
}

// An operator who redefines the `deep` chain has redefined what the
// hint means. That is the whole point of it being sugar.
func TestOverridingTheChainOverridesTheHint(t *testing.T) {
	t.Parallel()
	overridden := config.ChainConfig{
		Label: "deep",
		Steps: []config.ChainStepConfig{{Provider: "cheap"}},
	}
	n := chainNode(t, []config.ChainConfig{overridden}, "")
	got, err := n.resolver.Resolve(compute.ResolveRequest{Hint: compute.HintDeep})
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Provider.Label != "cheap" {
		t.Errorf("start = %q; the operator's definition of deep was ignored",
			got.Steps[0].Provider.Label)
	}
}

// A hint naming no chain must not invent a route. Falling through to
// ordinary matching keeps one mental model rather than two.
func TestAHintWithNoChainFallsThrough(t *testing.T) {
	t.Parallel()
	n := chainNode(t, []config.ChainConfig{deepChain()}, "")
	got, err := n.resolver.Resolve(compute.ResolveRequest{
		Complexity: 85,
		Hint:       compute.HintFast, // no "fast" chain configured
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChainLabel != "deep" {
		t.Errorf("chain = %q; an unmatched hint should fall through to triggers", got.ChainLabel)
	}
}

// A chain is atomic: a step below the floor rejects the whole chain,
// because a "reviewer step" on a weaker provider leaks the turn to
// that weaker tier.
func TestAHintedChainBelowTheFloorDoesNotBypassIt(t *testing.T) {
	t.Parallel()
	n := chainNode(t, []config.ChainConfig{{
		Label: "deep",
		Steps: []config.ChainStepConfig{{Provider: "cheap"}},
	}}, "")
	n.cfg.Compute.Providers[0].TrustTier = types.TrustPublic
	r, err := compute.NewResolver(&n.cfg.Compute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(compute.ResolveRequest{
		Hint:         compute.HintDeep,
		MinTrustTier: types.TrustLocal,
	})
	if err == nil && got != nil && got.ChainLabel == "deep" {
		t.Error("a hint routed the turn to a provider below the trust floor")
	}
}
