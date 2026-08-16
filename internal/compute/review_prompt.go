package compute

import (
	"fmt"
	"strings"
)

// The review prompt.
//
// Three things in it are load-bearing and worth naming, because each
// is a place where the obvious version produces sprawl:
//
// The PREFERENCE ORDER. Patching something that exists beats adding
// something new, every time. Left to itself a model writes a new skill
// per session, and the library becomes a pile of near-duplicates
// nobody can navigate.
//
// The NAMING RULE. A skill called "fix-issue-4021" or
// "debug-telegram-timeout" is a session artefact wearing a skill's
// clothes: it will never match another task, and it will sit in the
// index forever costing tokens on every turn.
//
// The AXIS SPLIT, which is the conceptual payload. Memory is state,
// skills are procedure, and — lobslaw has a third axis the references
// lack — SOUL is disposition. Recording "the user hates verbose
// replies" as a memory changes nothing next session, because nothing
// retrieves it at the moment it matters. Encoding it in the procedure
// that governs the task does.
//
// And one thing deliberately ABSENT: hermes's action bias, "a pass
// that does nothing is a missed learning opportunity, not a neutral
// outcome". That pressure is precisely what forced them to build a
// curator, usage telemetry, a stale/archive lifecycle and
// protected-skill rules. lobslaw has Dream to catch what a quiet pass
// missed, and propose mode means every marginal artefact costs
// somebody an approval. The bias here is conservative.

const reviewPromptBase = `You are reviewing a conversation that has already finished. The user has their reply; nothing is waiting on you.

Your job is to decide whether this exchange taught anything worth keeping — and usually it did not. A quiet pass is a correct and common outcome. Do not invent something to record.

## The three axes

- **Memory** is state: who the user is, what is currently true.
- **Skills** are procedure: how to do this CLASS of task, for this user.
- **Soul** is disposition: how to be. Not yours to change.

Frustration and style corrections are FIRST-CLASS SKILL SIGNALS, not just memory. "Stop giving me essays" recorded as a memory changes nothing next time, because nothing retrieves it at the moment it matters. Encoded into the procedure that governs the task, it does.

## Prefer amending to adding

In this order, and only fall through when the one above genuinely does not fit:

1. Refine a skill that already exists.
2. Add to an existing skill's body rather than splitting it.
3. Only then propose a new skill.

A library of near-duplicates is worse than a smaller one: every entry costs tokens on every turn, and two skills for one job contradict each other.

## Naming

Class-level names only. Never name a skill after:

- an issue or PR number ("fix-issue-4021")
- an error string ("resolve-econnreset")
- a codename or a one-off ("debug-telegram-timeout")

If the name only makes sense to somebody who was in this conversation, it is not a skill.

## Answer format

Reply with JSON and nothing else:

{"action": "none"}

or

{"action": "refine", "refines": "<id from the list below>", "name": "...", "description": "one line", "body": "the full procedure", "rationale": "why this is better than what is there"}

or

{"action": "new", "name": "...", "description": "one line", "body": "the full procedure", "distinct": true}

Set "distinct": true only if you have checked the list below and this is genuinely a different job. If something close already exists, refine it instead.`

// reviewPrompt assembles the system prompt for one review.
func reviewPrompt(axes reviewAxes, existing []ArtefactSummary) string {
	var b strings.Builder
	b.WriteString(reviewPromptBase)

	// The axis that triggered is stated, so a turn that qualified only
	// on tool volume is not asked to speculate about who the user is.
	b.WriteString("\n\n## What triggered this review\n\n")
	switch {
	case axes.skills && axes.memory:
		b.WriteString("This turn did a lot of work AND the conversation has run for a while. Consider both procedure and what you have learned about the user.\n")
	case axes.skills:
		b.WriteString("This turn did enough work that a procedure might be worth encoding. Focus on that; do not speculate about the user from one turn.\n")
	case axes.memory:
		b.WriteString("The conversation has run for a while. Consider whether a durable pattern has emerged — but a single exchange is not a pattern.\n")
	}

	b.WriteString("\n## Skills that already exist\n\n")
	if len(existing) == 0 {
		b.WriteString("(none yet — so anything you propose is necessarily new)\n")
	} else {
		// The complete list, not a relevant subset. A fork shown a
		// filtered view proposes duplicates of whatever the filter
		// missed, which is the failure this list exists to prevent.
		b.WriteString("This list is complete. If your idea is a variant of one of these, refine it rather than adding another.\n\n")
		for _, e := range existing {
			fmt.Fprintf(&b, "- `%s` — **%s**: %s\n", e.ID, e.Name, e.Description)
		}
	}
	return b.String()
}
