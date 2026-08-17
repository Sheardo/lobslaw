package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// tail_messages and compact_max_completion_tokens used to be settings.
//
// Each was a second cap on something another setting already capped,
// and the tighter of two caps wins SILENTLY — a tail_messages of 5
// beside a generous tail_tokens truncates history for a reason nobody
// can see in the config file. They are derived now, so an operator
// cannot set the pair to disagree. That is better than the validation
// error the alternative would have needed, which only tells somebody
// after they already got it wrong.

// The derivation has to reproduce what the removed setting defaulted
// to, or every existing deployment quietly changes behaviour.
func TestTheDerivedCapsPreserveTheOldDefaults(t *testing.T) {
	t.Parallel()
	// 4000 was DefaultTailTokens; 100 was the session tail default.
	if got := tailMessagesFor(compute.DefaultTailTokens); got != 100 {
		t.Errorf("tailMessagesFor(%d) = %d, want the previous default of 100",
			compute.DefaultTailTokens, got)
	}
}

// The TOKEN cap is the one that carries meaning. The message cap is an
// I/O guard, so it must be loose enough that it is not what binds.
func TestTheMessageCapDoesNotBindBeforeTheTokenCap(t *testing.T) {
	t.Parallel()
	for _, tokens := range []int{2000, 4000, 8000, 16000} {
		msgs := tailMessagesFor(tokens)
		// If every message were at the assumed floor, the messages
		// read would still fit the token budget. Anything larger and
		// the token budget trims first, which is the intent.
		if msgs*minTokensPerMessage > tokens {
			t.Errorf("tail_tokens=%d derives %d messages, which can exceed the token budget",
				tokens, msgs)
		}
	}
}

// Explicit 0 tail_tokens means "unbounded tokens" — but the read still
// needs a ceiling, or a long conversation loads entirely into memory
// before anything trims it.
func TestUnboundedTokensStillBoundsTheRead(t *testing.T) {
	t.Parallel()
	got := tailMessagesFor(0)
	if got <= 0 {
		t.Fatalf("tailMessagesFor(0) = %d; an unbounded read is not a cap", got)
	}
	if got != maxTailMessages {
		t.Errorf("tailMessagesFor(0) = %d, want the ceiling %d", got, maxTailMessages)
	}
}

// A tiny budget must not derive a cap so small the assistant loses the
// exchange it is in the middle of.
func TestATinyBudgetKeepsAWorkableFloor(t *testing.T) {
	t.Parallel()
	if got := tailMessagesFor(1); got != minTailMessages {
		t.Errorf("tailMessagesFor(1) = %d, want the floor %d", got, minTailMessages)
	}
}

// An enormous budget must not derive an unbounded read.
func TestAHugeBudgetIsStillCapped(t *testing.T) {
	t.Parallel()
	if got := tailMessagesFor(10_000_000); got != maxTailMessages {
		t.Errorf("tailMessagesFor(huge) = %d, want the ceiling %d", got, maxTailMessages)
	}
}

// The completion cap is a safety margin on the summary cap — the
// output is truncated to the summary cap regardless, so this only
// stops a model that ignores its instructions generating megabytes.
// It must never be TIGHTER than what it protects, or the summariser
// cannot fill the budget it was given.
func TestTheCompletionCapNeverUndercutsTheSummaryCap(t *testing.T) {
	t.Parallel()
	for _, summary := range []int{100, 600, 2000} {
		if got := completionCapFor(summary); got <= summary {
			t.Errorf("completionCapFor(%d) = %d; the summariser could not fill its own budget",
				summary, got)
		}
	}
}

// Unset stays unset, so the summariser takes its own default rather
// than being handed a derived zero that means something else.
func TestAnUnsetSummaryCapDerivesNothing(t *testing.T) {
	t.Parallel()
	if got := completionCapFor(0); got != 0 {
		t.Errorf("completionCapFor(0) = %d, want 0 so the default applies", got)
	}
}
