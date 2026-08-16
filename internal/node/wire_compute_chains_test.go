package node

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// `[[compute.chains]]` is parsed, validated for coherence, and then
// nothing routes through it: Resolver.Resolve has no callers, and the
// turn path is the provider backup chain, which knows about `backup`
// links and nothing about triggers or multi-step chains.
//
// That is the same shape as the trust floor before it was enforced —
// config that looks like it works. The difference is that a floor
// silently permitted something, where a chain silently does nothing;
// but an operator reading their own config cannot tell either way, and
// that is the part worth fixing now rather than when the routing
// lands.

func nodeWithChains(t *testing.T, chains []config.ChainConfig, defaultChain string) (*Node, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	n := &Node{
		log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	n.cfg.Compute = config.ComputeConfig{
		Providers: []config.ProviderConfig{{
			Label: "main", Endpoint: "https://example.invalid", Model: "m",
			TrustTier: types.TrustPrivate,
		}},
		Chains:       chains,
		DefaultChain: defaultChain,
	}
	return n, &logs
}

func TestAConfiguredChainIsReportedAsInert(t *testing.T) {
	t.Parallel()
	n, logs := nodeWithChains(t, []config.ChainConfig{{
		Label: "deep",
		Steps: []config.ChainStepConfig{{Provider: "main", Role: "primary"}},
	}}, "")

	if err := n.wireResolver(); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, "do NOT route turns") {
		t.Fatalf("no warning was emitted:\n%s", out)
	}
	// Naming the chain, because "chains do nothing" without saying
	// which leaves an operator checking a config they already read.
	if !strings.Contains(out, "deep") {
		t.Errorf("the warning does not name the chain:\n%s", out)
	}
	// And naming what IS used, or the operator is told what is broken
	// without being told what to look at instead.
	if !strings.Contains(out, "roles.main") || !strings.Contains(out, "backup") {
		t.Errorf("the warning does not say what routes instead:\n%s", out)
	}
}

// A deployment that never wrote a chain does not need telling about a
// feature it is not using. A boot warning that fires for everybody is
// one nobody reads.
func TestNoChainsMeansNoWarning(t *testing.T) {
	t.Parallel()
	n, logs := nodeWithChains(t, nil, "")

	if err := n.wireResolver(); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Errorf("a deployment with no chains was warned:\n%s", logs.String())
	}
}

// default_chain on its own is the same misunderstanding with no
// [[compute.chains]] block to make it obvious.
func TestADefaultChainAloneStillWarns(t *testing.T) {
	t.Parallel()
	n, logs := nodeWithChains(t, []config.ChainConfig{{
		Label: "fallback",
		Steps: []config.ChainStepConfig{{Provider: "main"}},
	}}, "fallback")

	if err := n.wireResolver(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "fallback") {
		t.Errorf("default_chain was not reported:\n%s", logs.String())
	}
}

// Warn, not refuse. A config accepted yesterday must not stop a node
// booting today, and a chain is inert rather than dangerous — the turn
// still runs, on the provider it would have run on anyway.
func TestInertChainsDoNotBlockBoot(t *testing.T) {
	t.Parallel()
	n, _ := nodeWithChains(t, []config.ChainConfig{{
		Label: "deep",
		Steps: []config.ChainStepConfig{{Provider: "main"}},
	}}, "")

	if err := n.wireResolver(); err != nil {
		t.Errorf("an inert chain blocked boot: %v", err)
	}
	if n.resolver == nil {
		t.Error("the resolver was not built; its validation is the reason to keep it")
	}
}

// The validation is why the resolver survives at all. A chain naming a
// provider that does not exist must still fail, or deleting the
// routing would silently start accepting broken chains.
func TestABrokenChainStillFailsToWire(t *testing.T) {
	t.Parallel()
	n, _ := nodeWithChains(t, nil, "nonexistent")
	n.cfg.Compute.Chains = []config.ChainConfig{{
		Label: "deep",
		Steps: []config.ChainStepConfig{{Provider: "no-such-provider"}},
	}}

	if err := n.wireResolver(); err == nil {
		t.Error("a chain naming an unknown provider wired cleanly")
	}
}
