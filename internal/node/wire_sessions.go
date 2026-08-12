package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/internal/memory"
)

// sessionStoreAdapter adapts memory.SessionService to the
// gateway.SessionStore interface. The two speak the same shape but
// can't import each other without a package cycle, so the adapter
// sits here at the wiring layer — same pattern as
// episodicIngesterAdapter.
//
// It also translates memory.ErrNotLeader into
// gateway.ErrSessionUnavailable, which is what lets the gateway log a
// follower's failed write at debug rather than warn without importing
// the memory package to recognise it.
type sessionStoreAdapter struct {
	inner *memory.SessionService
}

// newSessionStore returns a gateway.SessionStore backed by raft, or
// nil when this node has no local memory state to write to. A nil
// return leaves the channel on its in-memory buffer.
func (n *Node) newSessionStore() gateway.SessionStore {
	if n.raft == nil || n.store == nil {
		return nil
	}
	return &sessionStoreAdapter{
		inner: memory.NewSessionService(n.raft, n.store, memory.SessionConfig{
			MaxMessages: n.cfg.Gateway.SessionMaxMessages,
		}),
	}
}

func (a *sessionStoreAdapter) LoadTail(ctx context.Context, ref gateway.SessionRef, n int) ([]compute.Message, error) {
	msgs, err := a.inner.LoadTail(ctx, toMemoryRef(ref), n)
	if err != nil {
		return nil, err
	}
	out := make([]compute.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, compute.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toComputeToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		})
	}
	return out, nil
}

func (a *sessionStoreAdapter) Append(ctx context.Context, ref gateway.SessionRef, turnID string, msgs []compute.Message) error {
	out := make([]memory.TranscriptMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, memory.TranscriptMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toTranscriptToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		})
	}
	_, err := a.inner.Append(ctx, toMemoryRef(ref), turnID, out)
	return translateSessionErr(err)
}

func (a *sessionStoreAdapter) Forget(ctx context.Context, ref gateway.SessionRef) error {
	return translateSessionErr(a.inner.Forget(ctx, toMemoryRef(ref)))
}

func toMemoryRef(ref gateway.SessionRef) memory.SessionRef {
	return memory.SessionRef{
		Channel:   ref.Channel,
		ChannelID: ref.ChannelID,
		UserID:    ref.UserID,
	}
}

func toComputeToolCalls(in []memory.TranscriptToolCall) []compute.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]compute.ToolCall, 0, len(in))
	for _, tc := range in {
		out = append(out, compute.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	}
	return out
}

func toTranscriptToolCalls(in []compute.ToolCall) []memory.TranscriptToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]memory.TranscriptToolCall, 0, len(in))
	for _, tc := range in {
		out = append(out, memory.TranscriptToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
	}
	return out
}

// translateSessionErr maps the leader-only write failure onto the
// gateway's degradable sentinel, preserving the original text so the
// operator still sees which node to retry against.
func translateSessionErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, memory.ErrNotLeader) {
		return fmt.Errorf("%w: %s", gateway.ErrSessionUnavailable, err)
	}
	return err
}
