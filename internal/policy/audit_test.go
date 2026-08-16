package policy

import (
	"context"
	"strings"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// No evaluator is registered anywhere in production code today, so
// every conditioned rule is unevaluable. At decision time that is
// handled safely; the failure is that it was handled *silently*. An
// operator's time-of-day allow looks right in a listing, never grants,
// and explains itself only in a warn line nobody reads until
// something is already broken.

func TestAuditNamesUnevaluableRulesAndWhatTheyActuallyDo(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)

	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "office-hours-allow", Subject: "*", Action: "tool:exec",
		Resource: "deploy", Effect: "allow", Priority: 10,
		Conditions: []*lobslawv1.Condition{{Key: "time_of_day"}},
	})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "office-hours-deny", Subject: "*", Action: "tool:exec",
		Resource: "drop_table", Effect: "deny", Priority: 20,
		Conditions: []*lobslawv1.Condition{{Key: "time_of_day"}},
	})
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "plain-allow", Subject: "*", Action: "*",
		Resource: "*", Effect: "allow", Priority: 1,
	})

	defects, err := eng.UnevaluableRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(defects) != 2 {
		t.Fatalf("found %d defects, want 2 (the unconditioned rule is fine): %+v", len(defects), defects)
	}

	byID := map[string]RuleDefect{}
	for _, d := range defects {
		byID[d.RuleID] = d
	}
	if _, ok := byID["plain-allow"]; ok {
		t.Error("a rule with no conditions was reported as unevaluable")
	}

	// The wording is the feature. "condition evaluation error" is not
	// something an operator can act on; "will never grant" is.
	allow := byID["office-hours-allow"]
	if !strings.Contains(allow.Consequence, "never grant") {
		t.Errorf("allow consequence = %q; it does not say the rule never grants", allow.Consequence)
	}
	deny := byID["office-hours-deny"]
	if !strings.Contains(deny.Consequence, "unconditionally") {
		t.Errorf("deny consequence = %q; it does not say the rule denies unconditionally", deny.Consequence)
	}
	if len(allow.Keys) != 1 || allow.Keys[0] != "time_of_day" {
		t.Errorf("keys = %v, want the unresolvable key named", allow.Keys)
	}
}

// Registering the evaluator resolves the defect. Otherwise the audit
// would nag forever about rules that work.
func TestAuditClearsOnceTheEvaluatorExists(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "conditioned", Subject: "*", Action: "*", Resource: "*",
		Effect: "allow", Priority: 1,
		Conditions: []*lobslawv1.Condition{{Key: "time_of_day"}},
	})

	if defects, err := eng.UnevaluableRules(); err != nil || len(defects) != 1 {
		t.Fatalf("defects = %+v (err %v), want exactly one before registration", defects, err)
	}

	eng.RegisterCondition("time_of_day", func(context.Context, types.Condition) (bool, error) {
		return true, nil
	})

	defects, err := eng.UnevaluableRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(defects) != 0 {
		t.Errorf("still %d defects after registering the evaluator: %+v", len(defects), defects)
	}
}

// A rule naming several unknown keys reports all of them, so one fix
// per boot is not the workflow.
func TestAuditReportsEveryMissingKey(t *testing.T) {
	t.Parallel()
	eng, store := newTestEngine(t)
	seedRule(t, store, &lobslawv1.PolicyRule{
		Id: "multi", Subject: "*", Action: "*", Resource: "*",
		Effect: "deny", Priority: 1,
		Conditions: []*lobslawv1.Condition{
			{Key: "peer_cidr"}, {Key: "time_of_day"}, {Key: "peer_cidr"},
		},
	})

	defects, err := eng.UnevaluableRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(defects) != 1 {
		t.Fatalf("defects = %+v, want 1", defects)
	}
	// Sorted, so the log line is stable between boots and a diff of
	// two boots means something changed.
	got := strings.Join(defects[0].Keys, ",")
	if got != "peer_cidr,peer_cidr,time_of_day" {
		t.Errorf("keys = %q, want every missing key, sorted", got)
	}
}

// The safety argument for skipping an erroring ALLOW, asserted rather
// than reasoned about in a comment: skipping can only ever be more
// restrictive than applying, because applying an allow yields the most
// permissive effect there is. So whatever sits below it — a deny, a
// confirmation, or nothing at all — is never a wider grant.
func TestSkippingAnErroringAllowIsNeverMorePermissive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		beneath *lobslawv1.PolicyRule
		want    types.Effect
	}{
		{
			name: "a deny beneath it stands",
			beneath: &lobslawv1.PolicyRule{
				Id: "beneath", Subject: "*", Action: "*", Resource: "*",
				Effect: "deny", Priority: 1,
			},
			want: types.EffectDeny,
		},
		{
			name: "a confirmation beneath it stands",
			beneath: &lobslawv1.PolicyRule{
				Id: "beneath", Subject: "*", Action: "*", Resource: "*",
				Effect: "require_confirmation", Priority: 1,
			},
			want: types.EffectRequireConfirmation,
		},
		{
			name:    "nothing beneath it means default-deny",
			beneath: nil,
			want:    types.EffectDeny,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eng, store := newTestEngine(t)
			seedRule(t, store, &lobslawv1.PolicyRule{
				Id: "broken-allow", Subject: "*", Action: "*", Resource: "*",
				Effect: "allow", Priority: 100,
				Conditions: []*lobslawv1.Condition{{Key: "no_such_evaluator"}},
			})
			if tc.beneath != nil {
				seedRule(t, store, tc.beneath)
			}

			dec, err := eng.Evaluate(context.Background(),
				&types.Claims{UserID: "alice"}, "tool:exec", "anything")
			if err != nil {
				t.Fatal(err)
			}
			if dec.Effect != tc.want {
				t.Errorf("effect = %v, want %v", dec.Effect, tc.want)
			}
			if dec.Effect == types.EffectAllow {
				t.Error("an allow that could not be evaluated granted anyway")
			}
		})
	}
}
