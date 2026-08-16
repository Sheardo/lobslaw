package gateway

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// RaftPrompts is the durable Prompts implementation. A confirmation
// issued by one node can be answered on another, and survives the
// asking process restarting — neither of which the in-memory registry
// could do.
type RaftPrompts struct {
	store *memory.PromptStore
	// nodeID is recorded as the resolver. The channel handlers do not
	// carry the answering user's identity into Resolve, so this is the
	// coarsest true answer to "who closed this" — better in the audit
	// trail than an empty string that reads like nobody did.
	nodeID string
}

// NewRaftPrompts wraps a raft-backed store as the gateway registry.
func NewRaftPrompts(store *memory.PromptStore, nodeID string) *RaftPrompts {
	return &RaftPrompts{store: store, nodeID: nodeID}
}

func (r *RaftPrompts) Create(turnID, reason, channel string, ttl time.Duration) (*Prompt, error) {
	rec := &lobslawv1.PromptRecord{
		TurnId:  turnID,
		Reason:  reason,
		Channel: channel,
	}
	if ttl > 0 {
		rec.ExpiresAt = timestamppb.New(time.Now().Add(ttl))
	}
	out, err := r.store.Create(rec)
	if err != nil {
		return nil, err
	}
	return fromRecord(out), nil
}

func (r *RaftPrompts) Get(id string) (*Prompt, error) {
	rec, err := r.store.Get(id)
	if err != nil {
		return nil, translatePromptErr(err)
	}
	return fromRecord(rec), nil
}

func (r *RaftPrompts) Resolve(id string, decision PromptDecision) error {
	if decision != PromptApproved && decision != PromptDenied {
		return errors.New("prompt: Resolve accepts only Approved or Denied")
	}
	// Scope is set here, not carried through: the button that records
	// a lasting grant does that separately, and conflating the two
	// would make every approval a standing one.
	_, err := r.store.Resolve(id, toDecision(decision),
		lobslawv1.PromptScope_PROMPT_SCOPE_ONCE, r.nodeID)
	return translatePromptErr(err)
}

func (r *RaftPrompts) Wait(ctx context.Context, id string) (PromptDecision, error) {
	rec, err := r.store.Wait(ctx, id, 0)
	if err != nil {
		return PromptPending, translatePromptErr(err)
	}
	return fromDecision(rec.Decision), nil
}

func fromRecord(rec *lobslawv1.PromptRecord) *Prompt {
	p := &Prompt{
		ID:       rec.Id,
		TurnID:   rec.TurnId,
		Reason:   rec.Reason,
		Channel:  rec.Channel,
		Decision: fromDecision(rec.Decision),
	}
	if rec.CreatedAt != nil {
		p.CreatedAt = rec.CreatedAt.AsTime()
	}
	if rec.ExpiresAt != nil {
		p.ExpiresAt = rec.ExpiresAt.AsTime()
	}
	return p
}

func toDecision(d PromptDecision) lobslawv1.PromptDecision {
	switch d {
	case PromptApproved:
		return lobslawv1.PromptDecision_PROMPT_DECISION_APPROVED
	case PromptDenied:
		return lobslawv1.PromptDecision_PROMPT_DECISION_DENIED
	case PromptTimedOut:
		return lobslawv1.PromptDecision_PROMPT_DECISION_TIMED_OUT
	default:
		return lobslawv1.PromptDecision_PROMPT_DECISION_PENDING
	}
}

func fromDecision(d lobslawv1.PromptDecision) PromptDecision {
	switch d {
	case lobslawv1.PromptDecision_PROMPT_DECISION_APPROVED:
		return PromptApproved
	case lobslawv1.PromptDecision_PROMPT_DECISION_DENIED:
		return PromptDenied
	case lobslawv1.PromptDecision_PROMPT_DECISION_TIMED_OUT:
		return PromptTimedOut
	default:
		return PromptPending
	}
}

// translatePromptErr maps the store's sentinels onto the gateway's,
// which the channel handlers already switch on to pick user-facing
// wording. Anything else passes through untouched — collapsing an
// unknown failure into a known one is how "couldn't reach the leader"
// becomes "that prompt no longer exists".
func translatePromptErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memory.ErrPromptNotFound):
		return ErrPromptNotFound
	case errors.Is(err, memory.ErrPromptResolved):
		return ErrPromptResolved
	default:
		return err
	}
}
