package node

import (
	"context"

	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// pendingReviewSource counts what is waiting for a decision.
//
// Both halves, because they are one queue to the person draining it: a
// PROPOSED artefact and a refinement staged against a live one are
// both "somebody has to look at this", and the second is invisible in
// a state filter because the record itself is ACTIVE.
type pendingReviewSource struct{ store *memory.SelfTaughtStore }

func (p pendingReviewSource) Notices(_ context.Context, principal string) ([]gateway.Notice, error) {
	if p.store == nil {
		return nil, nil
	}
	live, err := p.store.List(memory.SelfTaughtQuery{})
	if err != nil {
		return nil, err
	}
	var proposals, refinements int
	for _, rec := range live {
		// Owner-scoped, like every other read. Somebody permitted to
		// receive notices is not thereby permitted to learn what a
		// different principal has pending — and an artefact with no
		// owner is nobody's private business, so it counts for
		// everyone.
		if rec.GetOwner() != "" && rec.GetOwner() != principal {
			continue
		}
		if rec.GetState() == lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
			proposals++
		}
		if rec.GetPending() != nil {
			refinements++
		}
	}
	return gateway.PendingReviewNotice(proposals, refinements), nil
}
