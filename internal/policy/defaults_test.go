package policy

import (
	"context"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Config-derived default rules.
//
// Held in memory rather than written to the rule bucket, which is
// raft-replicated operator intent — a rule derived from one node's
// config file is neither, and every node writing its own copy at boot
// would turn a local setting into contested cluster state.
//
// Evaluated last, so anything an operator wrote and anything an
// earlier approval minted outranks them. A default that could not be
// overridden would not be a default, and that is the property these
// tests exist to pin.

func confirmDefault() types.PolicyRule {
	return types.PolicyRule{
		ID:       "config:memory.write_approval",
		Subject:  "*",
		Action:   "memory:write",
		Resource: "*",
		Effect:   types.EffectRequireConfirmation,
		Priority: -1 << 30,
	}
}

func TestADefaultAppliesWhenNothingElseMatches(t *testing.T) {
	t.Parallel()
	eng, _ := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "memory:write", "episodic")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectRequireConfirmation {
		t.Errorf("effect = %v, want require_confirmation", dec.Effect)
	}
	if dec.RuleID != "config:memory.write_approval" {
		t.Errorf("rule = %q; the decision does not say where it came from", dec.RuleID)
	}
}

// An operator rule wins. This is the whole point of the mechanism: a
// config default that could not be overridden would be a hardcoded
// branch wearing a rule's clothes.
func TestAnOperatorRuleOutranksADefault(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "operator-allows-writes", Subject: "user:alice",
		Action: "memory:write", Resource: "*",
		Effect: "allow", Priority: 0,
	})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "memory:write", "episodic")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectAllow {
		t.Fatalf("effect = %v, want allow — the default beat an operator rule", dec.Effect)
	}
	if dec.RuleID != "operator-allows-writes" {
		t.Errorf("rule = %q", dec.RuleID)
	}
}

// Including a deny: an operator who forbids something must not find it
// merely prompted about.
func TestAnOperatorDenyOutranksADefault(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "operator-forbids", Subject: "*",
		Action: "memory:write", Resource: "*",
		Effect: "deny", Priority: 0,
	})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "memory:write", "episodic")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v, want deny", dec.Effect)
	}
}

// An approval-minted rule is priority 1, so "always" wins over the
// default. That is the path that makes the gate answerable once rather
// than forever.
func TestAnApprovalMintedRuleOutranksADefault(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "approval:prompt-1", Subject: "user:alice",
		Action: "memory:write", Resource: "episodic",
		Effect: "allow", Priority: 1,
	})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "memory:write", "episodic")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectAllow {
		t.Errorf("effect = %v; an approved 'always' was asked again", dec.Effect)
	}
}

// A default whose action nobody asked about must not leak into an
// unrelated decision.
func TestADefaultDoesNotAnswerAnotherQuestion(t *testing.T) {
	t.Parallel()
	eng, _ := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "tool:exec", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v; the memory default answered a tool:exec question", dec.Effect)
	}
}

// Replaces rather than appends, so a reload cannot accumulate
// duplicates of the same setting.
func TestSetDefaultsReplaces(t *testing.T) {
	t.Parallel()
	eng, _ := newTestEngine(t)
	eng.SetDefaults([]types.PolicyRule{confirmDefault()})
	eng.SetDefaults(nil)

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "memory:write", "episodic")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v; a cleared default still applied", dec.Effect)
	}
}

// No defaults at all is every deployment before this existed, and it
// must behave exactly as it did.
func TestNoDefaultsChangesNothing(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "allow-bash", Subject: "*", Action: "tool:exec", Resource: "bash",
		Effect: "allow", Priority: 0,
	})

	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "tool:exec", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectAllow {
		t.Errorf("effect = %v", dec.Effect)
	}
}
