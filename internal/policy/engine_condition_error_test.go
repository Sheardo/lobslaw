package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A rule whose conditions cannot be evaluated used to be skipped
// outright. For an allow rule that is fail-closed and correct — it is
// also the only case the original tests covered, which is how the
// other half survived: skipping a *deny* drops the protection the
// rule exists for, and evaluation then continues into whatever
// lower-priority allow sits underneath.
//
// Both routes into "cannot evaluate" are tested, because they are the
// two an attacker controls: register no evaluator for the key (write
// a rule naming a condition this build doesn't know), or make an
// existing evaluator fail.

// denyOverAllow seeds a high-priority conditioned deny above a
// catch-all allow — the shape of every "deny X except generally
// permitted" policy, and the shape that leaked.
func denyOverAllow(t *testing.T, eng *Engine, store *memory.Store, condKey, denyEffect string) Decision {
	t.Helper()
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "guard", Subject: "*", Action: "*", Resource: "*",
		Effect: denyEffect, Priority: 100,
		Conditions: []*lobslawv1.Condition{{Key: condKey}},
	})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "general-allow", Subject: "*", Action: "*", Resource: "*",
		Effect: "allow", Priority: 1,
	})
	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "shell", "exec")
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestDenyRuleAppliesWhenItsConditionErrors(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.RegisterCondition("broken", func(_ context.Context, _ types.Condition) (bool, error) {
		return false, errors.New("evaluator backend unavailable")
	})

	dec := denyOverAllow(t, eng, store, "broken", "deny")
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v, want deny — a deny whose condition errored fell through to the allow beneath it", dec.Effect)
	}
	if dec.RuleID != "guard" {
		t.Errorf("RuleID = %q, want guard", dec.RuleID)
	}
}

// The same hole, reached without needing an evaluator to break: write
// a rule whose condition key this build has no evaluator for.
func TestDenyRuleAppliesWhenItsConditionIsUnknown(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)

	dec := denyOverAllow(t, eng, store, "condition_from_the_future", "deny")
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v, want deny — an unrecognised condition key disarmed the deny", dec.Effect)
	}
}

// require_confirmation is restrictive relative to allow, so the same
// reasoning applies: if we cannot tell whether the gate applies, ask.
func TestRequireConfirmationAppliesWhenItsConditionErrors(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.RegisterCondition("broken", func(_ context.Context, _ types.Condition) (bool, error) {
		return false, errors.New("nope")
	})

	dec := denyOverAllow(t, eng, store, "broken", "require_confirmation")
	if dec.Effect != types.EffectRequireConfirmation {
		t.Errorf("effect = %v, want require_confirmation", dec.Effect)
	}
}

// The other direction, so the fix does not over-correct: an allow
// whose condition errors must still be skipped rather than granted.
func TestAllowRuleIsStillSkippedWhenItsConditionErrors(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.RegisterCondition("broken", func(_ context.Context, _ types.Condition) (bool, error) {
		return false, errors.New("nope")
	})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "conditional-allow", Subject: "*", Action: "*", Resource: "*",
		Effect: "allow", Priority: 100,
		Conditions: []*lobslawv1.Condition{{Key: "broken"}},
	})
	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "shell", "exec")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %v, want deny — an allow that could not be evaluated must not grant", dec.Effect)
	}
	if dec.RuleID != "" {
		t.Errorf("RuleID = %q, want empty (default-deny, not the conditioned rule)", dec.RuleID)
	}
}

// A condition that evaluates cleanly to false is a definite "this
// rule does not apply" and must still skip, deny or not — otherwise
// every conditioned deny becomes unconditional.
func TestDenyRuleIsSkippedWhenItsConditionIsCleanlyFalse(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	eng.RegisterCondition("never", func(_ context.Context, _ types.Condition) (bool, error) {
		return false, nil
	})

	dec := denyOverAllow(t, eng, store, "never", "deny")
	if dec.RuleID != "general-allow" {
		t.Errorf("RuleID = %q, want general-allow — a condition that evaluated false should skip its rule", dec.RuleID)
	}
}
