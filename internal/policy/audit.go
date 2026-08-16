package policy

import (
	"fmt"
	"sort"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A condition whose key has no registered evaluator cannot be
// evaluated, and Evaluate then falls back on the rule's effect: a deny
// applies without evaluating (safe), an allow is skipped (also safe,
// because skipping can only ever be more restrictive than applying).
//
// Both are the right call at decision time and both are silent at
// warn level, which is the problem. Today NO evaluator is registered
// anywhere, so every conditioned rule is in this state — an operator
// who writes a time-of-day allow gets a rule that never grants, looks
// correct in a listing, and explains itself only in a log line nobody
// reads until something is already broken.
//
// So it is said once, loudly, at boot.

// RuleDefect is one rule that cannot do what it appears to do.
type RuleDefect struct {
	RuleID string
	Effect types.Effect
	// Keys are the condition keys with no registered evaluator.
	Keys []string
	// Consequence is the operator-facing description of what this
	// rule actually does, as opposed to what it looks like it does.
	Consequence string
}

func (d RuleDefect) String() string {
	return fmt.Sprintf("rule %q (%s): no evaluator for %v — %s",
		d.RuleID, d.Effect, d.Keys, d.Consequence)
}

// UnevaluableRules lists every stored rule that names a condition key
// this build has no evaluator for.
//
// Call after every RegisterCondition has run, since registration is
// allowed to happen after NewEngine — asking earlier would report
// defects that resolve moments later.
func (e *Engine) UnevaluableRules() ([]RuleDefect, error) {
	rules, err := e.loadRules()
	if err != nil {
		return nil, fmt.Errorf("audit rules: %w", err)
	}

	var out []RuleDefect
	for _, rule := range rules {
		var missing []string
		for _, c := range rule.Conditions {
			if _, ok := e.lookupEvaluator(c.Key); !ok {
				missing = append(missing, c.Key)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		out = append(out, RuleDefect{
			RuleID:      rule.ID,
			Effect:      rule.Effect,
			Keys:        missing,
			Consequence: consequenceOf(rule.Effect),
		})
	}
	return out, nil
}

// consequenceOf spells out what an unevaluable rule of each effect
// actually does. The wording is the point: "will never grant" is
// something an operator can act on; "condition evaluation error" is
// not.
func consequenceOf(effect types.Effect) string {
	switch effect {
	case types.EffectAllow:
		return "this rule will never grant anything; requests fall through to lower-priority rules and ultimately to default-deny"
	case types.EffectDeny:
		return "this rule denies unconditionally, as though its conditions were not there"
	case types.EffectRequireConfirmation:
		return "this rule always asks, as though its conditions were not there"
	default:
		return "this rule does not behave as written"
	}
}

// LogUnevaluableRules reports the audit at boot. Error level, not
// warn: a rule that cannot do what it says is a configuration fault
// somebody needs to fix, and warn is where this hid before.
func (e *Engine) LogUnevaluableRules() {
	defects, err := e.UnevaluableRules()
	if err != nil {
		e.logger.Error("policy: could not audit rules for unevaluable conditions", "err", err)
		return
	}
	if len(defects) == 0 {
		return
	}
	e.logger.Error("policy: rules reference conditions this build cannot evaluate; "+
		"they do not behave as written",
		"count", len(defects))
	for _, d := range defects {
		e.logger.Error("policy: unevaluable rule",
			"rule_id", d.RuleID,
			"effect", d.Effect,
			"condition_keys", d.Keys,
			"consequence", d.Consequence)
	}
}
