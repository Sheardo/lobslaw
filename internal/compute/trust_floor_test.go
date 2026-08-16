package compute

import (
	"errors"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// `min_trust_tier` was parsed, validated, logged at boot and rendered
// into the system prompt — and enforced nowhere. The only code that
// checked it was Resolver.buildDecision, and nothing calls
// Resolver.Resolve: the turn path is the provider backup chain, which
// walked label to label with no notion of a tier.
//
// So an operator could set a floor, watch it appear in the prompt, and
// have a turn silently complete on a public provider the moment the
// primary returned a 429.

func TestAnUnsetFloorPermitsEverything(t *testing.T) {
	t.Parallel()
	for _, tier := range []types.TrustTier{types.TrustLocal, types.TrustPrivate, types.TrustPublic, ""} {
		if !MeetsFloor("", tier) {
			t.Errorf("tier %q was refused with no floor set", tier)
		}
	}
}

func TestTheFloorIsHonouredInBothDirections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		floor, tier types.TrustTier
		want        bool
	}{
		{types.TrustPrivate, types.TrustLocal, true},
		{types.TrustPrivate, types.TrustPrivate, true},
		{types.TrustPrivate, types.TrustPublic, false},
		{types.TrustLocal, types.TrustPrivate, false},
		{types.TrustPublic, types.TrustPublic, true},
	}
	for _, c := range cases {
		if got := MeetsFloor(c.floor, c.tier); got != c.want {
			t.Errorf("MeetsFloor(%q, %q) = %v, want %v", c.floor, c.tier, got, c.want)
		}
	}
}

// A typo is not an opt-out. The operator asked for a restriction and
// wrote a string nothing recognises; treating that as "no floor" would
// grant the exact opposite of what they intended.
func TestAnInvalidFloorPermitsNothing(t *testing.T) {
	t.Parallel()
	if MeetsFloor("privat", types.TrustLocal) {
		t.Error("a typo'd floor permitted the most-trusted tier")
	}
	if MeetsFloor("privat", types.TrustPublic) {
		t.Error("a typo'd floor permitted a public provider")
	}
}

// An undeclared tier is not evidence of a high one.
func TestAProviderWithNoTierFailsAnySetFloor(t *testing.T) {
	t.Parallel()
	if MeetsFloor(types.TrustPublic, "") {
		t.Error("a provider with no declared tier passed a floor")
	}
	if MeetsFloor(types.TrustPublic, "nonsense") {
		t.Error("a provider with an invalid tier passed a floor")
	}
}

// The soul is tunable at runtime. Reading the floor once at
// construction would pin it to boot, and an operator raising it would
// find the change took effect in the prompt and not in the routing.
func TestTheFloorIsReadPerCall(t *testing.T) {
	t.Parallel()
	floor := types.TrustPublic
	get := func() *types.SoulConfig { return &types.SoulConfig{MinTrustTier: floor} }

	if got := FloorOf(get); got != types.TrustPublic {
		t.Fatalf("got %q", got)
	}
	floor = types.TrustLocal
	if got := FloorOf(get); got != types.TrustLocal {
		t.Errorf("got %q; the floor was captured rather than read", got)
	}
}

func TestFloorOfToleratesNilAtEveryLevel(t *testing.T) {
	t.Parallel()
	if got := FloorOf(nil); got != "" {
		t.Errorf("nil accessor = %q", got)
	}
	if got := FloorOf(func() *types.SoulConfig { return nil }); got != "" {
		t.Errorf("nil soul = %q", got)
	}
}

// --- the error -----------------------------------------------------

// Its own error, not a generic "all providers failed": one is an
// outage, the other is a configuration that cannot serve a turn, and
// no amount of waiting fixes the second.
func TestTheFloorErrorNamesEveryCandidateAndItsTier(t *testing.T) {
	t.Parallel()
	err := &ErrBelowTrustFloor{
		Floor: types.TrustPrivate,
		Considered: []TrustCandidate{
			{Label: "openrouter", Tier: types.TrustPublic},
			{Label: "groq", Tier: types.TrustPublic},
		},
	}
	msg := err.Error()
	for _, want := range []string{"private", "openrouter(public)", "groq(public)", "min_trust_tier"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q; missing %q", msg, want)
		}
	}
}

// Distinguishable by type, so a caller can tell an operator to fix
// config rather than to wait.
func TestTheFloorErrorIsDistinguishable(t *testing.T) {
	t.Parallel()
	var target *ErrBelowTrustFloor
	wrapped := errors.Join(errors.New("context"), &ErrBelowTrustFloor{Floor: types.TrustLocal})
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not find it through a wrap")
	}
	if target.Floor != types.TrustLocal {
		t.Errorf("floor = %q", target.Floor)
	}
}

func TestTheFloorErrorReadsWithNoCandidates(t *testing.T) {
	t.Parallel()
	msg := (&ErrBelowTrustFloor{Floor: types.TrustLocal}).Error()
	if !strings.Contains(msg, "local") || strings.Contains(msg, "considered ") {
		t.Errorf("err = %q", msg)
	}
}
