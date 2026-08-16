package compute

import (
	"context"
	"errors"
	"strings"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Staging what the agent writes to memory.
//
// Memory that silently self-modifies and cannot be inspected is a
// trust problem for a privacy-first product. `lobslaw memory list` and
// the consolidation log answer "what happened"; neither answers "may
// this happen", and the write lands either way.
//
// So a tool can be marked as needing approval before it runs. The gate
// is a POLICY question rather than a branch inside the tool, which is
// what makes the answer reusable: a session grant covers the rest of
// the conversation, an "always" mints a visible and revocable rule,
// and an operator who wants something narrower writes an ordinary
// policy rule that outranks the config default.
//
// The alternative — a check inside memory_write consulting a bool —
// would have needed its own notion of "already approved", and would
// have grown a second, subtly different approval system beside the one
// R2 built.

// ApprovalAction is the policy action a gated write is checked under.
//
// Distinct from tool:exec, deliberately. A deployment allows tool:exec
// on memory_write in the ordinary way — otherwise the tool could not
// run at all — so reusing that action would mean the allow rule
// already in place silently satisfied the gate.
const ApprovalAction = "memory:write"

// gatedTool is what an extra approval check needs to know.
type gatedTool struct {
	action   string
	resource string
	// summarise turns the call's parameters into something a person
	// can decide about. A confirmation that says only "the agent wants
	// to write a memory" is one nobody can answer usefully, so they
	// answer it reflexively — which is worse than not asking.
	summarise func(map[string]string) string
}

// RequireApproval marks a tool as needing confirmation before it runs.
//
// Additive to the ordinary tool:exec check rather than a replacement:
// a denied tool stays denied, and this only ever adds a question.
//
// The ACTION is not a parameter. Passing one would let a caller supply
// "tool:exec" — the action every deployment already allows for this
// tool, since otherwise it could not run — and the gate would be
// silently satisfied by a rule that has nothing to do with it. There is
// no configuration in which that is the right thing to pass, so it is
// not offered.
func (e *Executor) RequireApproval(tool, resource string, summarise func(map[string]string) string) {
	e.gateMu.Lock()
	defer e.gateMu.Unlock()
	if e.gated == nil {
		e.gated = map[string]gatedTool{}
	}
	e.gated[tool] = gatedTool{action: ApprovalAction, resource: resource, summarise: summarise}
}

// approvalFor returns the gate for a tool, if it has one.
func (e *Executor) approvalFor(tool string) (gatedTool, bool) {
	e.gateMu.RLock()
	defer e.gateMu.RUnlock()
	g, ok := e.gated[tool]
	return g, ok
}

// checkWriteApproval runs the extra gate, if the tool has one.
//
// Returns ErrRequireConfirm carrying a summary of WHAT is being
// written, because the decision is about the content and a prompt that
// withholds it cannot be answered.
func (e *Executor) checkWriteApproval(ctx context.Context, claims *types.Claims, tool string, params map[string]string) error {
	gate, ok := e.approvalFor(tool)
	if !ok {
		return nil
	}
	err := e.policyAllow(ctx, claims, gate.action, gate.resource)
	if err == nil {
		return nil
	}
	// A confirmation gets the summary appended so the prompt says what
	// is being written. Any other outcome — a deny, an engine failure
	// — passes through untouched: adding content to a denial would put
	// it in front of somebody who is not being asked to decide.
	if !errors.Is(err, ErrRequireConfirm) {
		return err
	}
	if gate.summarise == nil {
		return err
	}
	summary := gate.summarise(params)
	if summary == "" {
		return err
	}
	return &confirmWithSummary{inner: err, summary: summary}
}

// confirmWithSummary carries the human-facing description alongside
// the sentinel, so errors.Is still finds ErrRequireConfirm.
type confirmWithSummary struct {
	inner   error
	summary string
}

func (c *confirmWithSummary) Error() string { return c.inner.Error() + ": " + c.summary }
func (c *confirmWithSummary) Unwrap() error { return c.inner }

// MemoryWriteSummary renders a memory_write call for a confirmation
// prompt.
//
// Content DOES appear here, unlike in a trace span, and the difference
// is the audience: a span goes to whatever telemetry the operator
// runs, while this goes to the person being asked to approve the
// write. Withholding it would make the question unanswerable.
//
// Truncated, because a confirmation is a prompt and a prompt that is
// three screens long is one nobody reads to the end.
func MemoryWriteSummary(params map[string]string) string {
	event := strings.TrimSpace(params["event"])
	if event == "" {
		return ""
	}
	const cap = 200
	if len([]rune(event)) > cap {
		event = string([]rune(event)[:cap-1]) + "…"
	}
	var b strings.Builder
	b.WriteString("remember: ")
	b.WriteString(event)
	if tags := strings.TrimSpace(params["tags"]); tags != "" && tags != "[]" {
		b.WriteString(" (tags ")
		b.WriteString(tags)
		b.WriteString(")")
	}
	return b.String()
}

// WriteApprovalDefault is the policy rule the config flag installs.
//
// A rule rather than a hardcoded branch, so it composes: an operator
// can override it with anything of higher priority, an approval can
// mint an allow that outranks it, and it shows up wherever rules show
// up rather than being invisible behaviour.
//
// Priority is deliberately the lowest the type allows. A default that
// could outrank an operator's rule would not be a default.
func WriteApprovalDefault() types.PolicyRule {
	return types.PolicyRule{
		ID:       "config:memory.write_approval",
		Subject:  "*",
		Action:   ApprovalAction,
		Resource: "*",
		Effect:   types.EffectRequireConfirmation,
		Priority: -1 << 30,
	}
}
