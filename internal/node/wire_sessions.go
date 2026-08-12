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

// newSessionCompactor builds the compaction hook. Returns nil when
// anything it needs is missing — no raft/store (nothing to compact),
// or no summariser role resolved (nothing to compact with). A nil
// compactor means long conversations lose their head to the context
// budget instead of being summarised, which is the pre-compaction
// behaviour rather than a failure.
func (n *Node) newSessionCompactor() gateway.SessionCompactor {
	if n.raft == nil || n.store == nil || n.roleMap == nil {
		return nil
	}
	provider := n.roleMap.For(compute.RoleSummariser)
	if provider == nil {
		return nil
	}
	svc := memory.NewSessionService(n.raft, n.store, memory.SessionConfig{
		MaxMessages: n.cfg.Gateway.SessionMaxMessages,
	})
	cfg := n.cfg.Compute.Context
	inner := compute.NewCompactor(
		&sessionSummaryAdapter{inner: svc},
		compute.NewLLMSummarizer(provider, ""),
		compute.CompactorConfig{
			KeepMessages:     derefInt(cfg.CompactKeepMessages),
			TriggerTokens:    derefInt(cfg.CompactTriggerTokens),
			MaxSummaryTokens: derefInt(cfg.CompactMaxSummaryTokens),
			Logger:           n.log,
		})
	if inner == nil {
		return nil
	}
	return &compactorAdapter{inner: inner}
}

// derefInt reads an optional config int; nil means "take the default",
// which the compute-side constructor applies.
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func (a *sessionStoreAdapter) LoadTranscript(ctx context.Context, ref gateway.SessionRef, n int) (gateway.Transcript, error) {
	t, err := a.inner.LoadTranscript(ctx, toMemoryRef(ref))
	if err != nil {
		return gateway.Transcript{}, err
	}
	msgs := t.Messages
	if n > 0 && len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	return gateway.Transcript{
		Summary:  t.Summary,
		Messages: toComputeMessages(msgs),
	}, nil
}

// compactorAdapter bridges the gateway's compaction hook to the
// compute-side Compactor, which owns the summariser.
type compactorAdapter struct {
	inner *compute.Compactor
}

func (a *compactorAdapter) MaybeCompact(ctx context.Context, ref gateway.SessionRef) (bool, error) {
	ok, err := a.inner.MaybeCompact(ctx, compute.SessionKey{
		Channel:   ref.Channel,
		ChannelID: ref.ChannelID,
	})
	return ok, translateSessionErr(err)
}

// sessionSummaryAdapter exposes the session store to the compute-side
// compactor. compute can't take memory's types directly without the
// import cycle the rest of this file works around.
type sessionSummaryAdapter struct {
	inner *memory.SessionService
}

func toMemoryKey(k compute.SessionKey) memory.SessionRef {
	return memory.SessionRef{Channel: k.Channel, ChannelID: k.ChannelID}
}

func (a *sessionSummaryAdapter) Pending(ctx context.Context, k compute.SessionKey) (string, uint64, uint64, error) {
	t, err := a.inner.LoadTranscript(ctx, toMemoryKey(k))
	if err != nil {
		return "", 0, 0, err
	}
	return t.Summary, t.SummaryThroughSeq, t.NextSeq, nil
}

func (a *sessionSummaryAdapter) Range(ctx context.Context, k compute.SessionKey, after, through uint64) ([]compute.Message, error) {
	msgs, err := a.inner.LoadRange(ctx, toMemoryKey(k), after, through)
	if err != nil {
		return nil, err
	}
	return toComputeMessages(msgs), nil
}

func (a *sessionSummaryAdapter) PutSummary(ctx context.Context, k compute.SessionKey, summary string, through uint64) error {
	return a.inner.PutSummary(ctx, toMemoryKey(k), summary, through)
}

func toComputeMessages(in []memory.TranscriptMessage) []compute.Message {
	out := make([]compute.Message, 0, len(in))
	for _, m := range in {
		out = append(out, compute.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toComputeToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		})
	}
	return out
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
