package compute

import (
	"context"
	"sync"
)

// Every confirmation was one-shot: PromptDecision was approved or
// denied and nothing else, so the operator was asked again the next
// time the same tool ran on the same thing. Forever.
//
// That is not a small annoyance. A confirmation someone has already
// answered a dozen times stops being a decision and becomes a reflex,
// and an operator who switches confirmations off to stop the nagging
// has lost the protection entirely. The gate is only worth having if
// it asks about things the answer might actually differ on.
//
// So an approval can also cover the rest of the conversation.
//
// A permanent "always" is deliberately absent rather than added here.
// The policy engine already evaluates (subject, action, resource) and
// already has allow as an effect, so a permanent approval is an allow
// rule — after which this check never fires again, because Evaluate
// returns allow before it reaches require_confirmation. Building a
// second permanent store beside it would mean two things deciding the
// same question, and eventually disagreeing.

// SessionApprovals records "approved for the rest of this
// conversation" grants. Safe for concurrent use.
//
// Keyed by conversation rather than by user: the confirmation was
// shown in a conversation and answered there, and in a group chat the
// person who taps Approve is approving for that chat. Widening it to
// the user across every conversation would grant more than the button
// appeared to offer.
//
// Grants live only in this process, deliberately. A restart ends the
// continuity the user was reasoning about, and a grant that outlives
// what they were looking at is one they did not knowingly give.
//
// NOTE for whoever adds a "forget this conversation" command: it must
// drop that conversation's grants too, or a cleared conversation keeps
// privileges the user believes they revoked. There is no such command
// today, which is why there is no hook for it here.
type SessionApprovals struct {
	mu      sync.RWMutex
	granted map[string]struct{}
}

// NewSessionApprovals builds an empty store.
func NewSessionApprovals() *SessionApprovals {
	return &SessionApprovals{granted: make(map[string]struct{})}
}

// Grant records an approval for the conversation identified by the
// turn on ctx, and reports whether it recorded one. A turn with no
// identity is refused: an anonymous grant has no conversation to be
// scoped to, and a grant that matches everything is the opposite of
// what the user was offered.
func (s *SessionApprovals) Grant(ctx context.Context, action, resource string) bool {
	key, ok := approvalKey(ctx, action, resource)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.granted[key] = struct{}{}
	return true
}

// Granted reports whether this conversation has already approved this
// operation. A nil store grants nothing, so the zero value is the safe
// one and a deployment that never wires a store is not accidentally
// permissive.
func (s *SessionApprovals) Granted(ctx context.Context, action, resource string) bool {
	if s == nil {
		return false
	}
	key, ok := approvalKey(ctx, action, resource)
	if !ok {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, found := s.granted[key]
	return found
}

// approvalKey derives the grant key from the turn identity on ctx.
//
// The identity comes from the request context rather than from
// anything the model produced, for the same reason ownership does: a
// key the model could influence would let a prompt injection claim a
// grant belonging to a different conversation.
func approvalKey(ctx context.Context, action, resource string) (string, bool) {
	id, ok := TurnIdentityFrom(ctx)
	if !ok || id.Channel == "" || id.ChannelID == "" {
		return "", false
	}
	// NUL separates the conversation from the operation, so a channel
	// id containing the separator cannot forge a different key.
	return id.Channel + ":" + id.ChannelID + "\x00" + action + "\x00" + resource, true
}
