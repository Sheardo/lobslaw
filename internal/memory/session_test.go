package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testSessionService(t *testing.T, maxMessages int) *SessionService {
	t.Helper()
	svc := newTestServiceStack(t)
	return NewSessionService(svc.raft, svc.store, SessionConfig{MaxMessages: maxMessages})
}

func userMsg(text string) TranscriptMessage {
	return TranscriptMessage{Role: "user", Content: text}
}

func assistantMsg(text string) TranscriptMessage {
	return TranscriptMessage{Role: "assistant", Content: text}
}

func TestSessionAppendLoadRoundTrip(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "telegram", ChannelID: "12345", UserID: "alice"}

	if _, err := s.Append(ctx, ref, "turn-1", []TranscriptMessage{
		userMsg("hello"),
		assistantMsg("hi there"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Errorf("first message = %+v, want user/hello", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "hi there" {
		t.Errorf("second message = %+v, want assistant/hi there", got[1])
	}
}

func TestSessionLoadMissingIsNotAnError(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	got, err := s.Load(context.Background(), SessionRef{Channel: "rest", ChannelID: "nobody"})
	if err != nil {
		t.Fatalf("Load on absent session: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0", len(got))
	}
}

func TestSessionAppendPreservesOrderAcrossTurns(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "telegram", ChannelID: "1"}

	for i := range 5 {
		if _, err := s.Append(ctx, ref, fmt.Sprintf("turn-%d", i), []TranscriptMessage{
			userMsg(fmt.Sprintf("q%d", i)),
			assistantMsg(fmt.Sprintf("a%d", i)),
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d messages, want 10", len(got))
	}
	for i := range 5 {
		if want := fmt.Sprintf("q%d", i); got[i*2].Content != want {
			t.Errorf("message %d = %q, want %q", i*2, got[i*2].Content, want)
		}
		if want := fmt.Sprintf("a%d", i); got[i*2+1].Content != want {
			t.Errorf("message %d = %q, want %q", i*2+1, got[i*2+1].Content, want)
		}
	}
}

// Ordering must survive the seq crossing a digit boundary — the whole
// point of zero-padding the key. Without padding, "…:10" sorts before
// "…:9" and the transcript silently scrambles after nine messages.
func TestSessionOrderingSurvivesDigitRollover(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "rest", ChannelID: "rollover"}

	for i := range 12 {
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{userMsg(fmt.Sprintf("%02d", i))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("got %d messages, want 12", len(got))
	}
	for i := range 12 {
		if want := fmt.Sprintf("%02d", i); got[i].Content != want {
			t.Errorf("position %d = %q, want %q (ordering scrambled)", i, got[i].Content, want)
		}
	}
}

func TestSessionTrimsToMaxMessages(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 4)
	ctx := context.Background()
	ref := SessionRef{Channel: "telegram", ChannelID: "trim"}

	for i := range 5 {
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{
			userMsg(fmt.Sprintf("q%d", i)),
			assistantMsg(fmt.Sprintf("a%d", i)),
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4 (the cap)", len(got))
	}
	// Oldest dropped, newest kept.
	want := []string{"q3", "a3", "q4", "a4"}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("position %d = %q, want %q", i, got[i].Content, w)
		}
	}
}

// Trimming must actually delete the evicted records, not just move
// the FirstSeq cursor past them — otherwise a busy chat leaks rows
// into the shared bucket forever.
func TestSessionTrimDeletesEvictedRecords(t *testing.T) {
	t.Parallel()
	svc := newTestServiceStack(t)
	s := NewSessionService(svc.raft, svc.store, SessionConfig{MaxMessages: 2})
	ctx := context.Background()
	ref := SessionRef{Channel: "telegram", ChannelID: "leak"}

	for i := range 6 {
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{userMsg(fmt.Sprintf("m%d", i))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	var stored int
	err := svc.store.ForEachPrefix(BucketSessionMessages, "telegram:leak:", func(string, []byte) error {
		stored++
		return nil
	})
	if err != nil {
		t.Fatalf("ForEachPrefix: %v", err)
	}
	if stored != 2 {
		t.Errorf("%d message records on disk, want 2 — evicted records leaked", stored)
	}
}

func TestSessionToolCallsRoundTrip(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "rest", ChannelID: "tools"}

	if _, err := s.Append(ctx, ref, "turn-1", []TranscriptMessage{
		userMsg("what's in /tmp?"),
		{
			Role: "assistant",
			ToolCalls: []TranscriptToolCall{
				{ID: "call_1", Name: "list_files", Arguments: `{"path":"/tmp"}`},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt\nb.txt"},
		assistantMsg("two files."),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if len(got[1].ToolCalls) != 1 {
		t.Fatalf("assistant message lost its tool calls: %+v", got[1])
	}
	tc := got[1].ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "list_files" || tc.Arguments != `{"path":"/tmp"}` {
		t.Errorf("tool call = %+v, want call_1/list_files with args", tc)
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call_1" {
		t.Errorf("tool result = %+v, want role=tool linked to call_1", got[2])
	}
}

// The system prompt is rebuilt per turn by promptgen from live SOUL +
// tool state; persisting a copy would replay a stale identity.
func TestSessionDropsSystemMessages(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "rest", ChannelID: "sys"}

	if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{
		{Role: "system", Content: "you are lobslaw"},
		userMsg("hi"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (system dropped)", len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("retained %q, want the user message", got[0].Role)
	}
}

func TestSessionLoadTailLimits(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	ref := SessionRef{Channel: "rest", ChannelID: "tail"}

	for i := range 10 {
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{userMsg(fmt.Sprintf("m%d", i))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := s.LoadTail(ctx, ref, 3)
	if err != nil {
		t.Fatalf("LoadTail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	if got[0].Content != "m7" || got[2].Content != "m9" {
		t.Errorf("tail = %q..%q, want m7..m9", got[0].Content, got[2].Content)
	}
}

func TestSessionForgetPurgesTranscript(t *testing.T) {
	t.Parallel()
	svc := newTestServiceStack(t)
	s := NewSessionService(svc.raft, svc.store, SessionConfig{})
	ctx := context.Background()
	ref := SessionRef{Channel: "telegram", ChannelID: "gone"}

	for i := range 3 {
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{userMsg(fmt.Sprintf("m%d", i))}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := s.Forget(ctx, ref); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load after Forget: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d messages after Forget, want 0", len(got))
	}

	var leftover int
	if err := svc.store.ForEachPrefix(BucketSessionMessages, "telegram:gone:", func(string, []byte) error {
		leftover++
		return nil
	}); err != nil {
		t.Fatalf("ForEachPrefix: %v", err)
	}
	if leftover != 0 {
		t.Errorf("%d orphaned message records after Forget, want 0", leftover)
	}
}

// Sessions share one bucket, so a prefix that isn't ':'-terminated
// would let "rest:1" read "rest:10"'s transcript.
func TestSessionsDoNotBleedAcrossSimilarIDs(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	short := SessionRef{Channel: "rest", ChannelID: "1"}
	long := SessionRef{Channel: "rest", ChannelID: "10"}

	if _, err := s.Append(ctx, short, "t", []TranscriptMessage{userMsg("i am one")}); err != nil {
		t.Fatalf("Append short: %v", err)
	}
	if _, err := s.Append(ctx, long, "t", []TranscriptMessage{userMsg("i am ten")}); err != nil {
		t.Fatalf("Append long: %v", err)
	}

	got, err := s.Load(ctx, short)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Content != "i am one" {
		t.Errorf("session rest:1 = %+v, want just its own message", got)
	}
}

func TestSessionRejectsColonInComponents(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()
	_, err := s.Append(ctx, SessionRef{Channel: "tele:gram", ChannelID: "1"}, "t",
		[]TranscriptMessage{userMsg("x")})
	if err == nil || !strings.Contains(err.Error(), "':'") {
		t.Errorf("expected colon rejection, got %v", err)
	}
}

func TestSessionListReturnsIndexRecords(t *testing.T) {
	t.Parallel()
	s := testSessionService(t, 0)
	ctx := context.Background()

	for _, id := range []string{"a", "b"} {
		ref := SessionRef{Channel: "telegram", ChannelID: id, UserID: "alice"}
		if _, err := s.Append(ctx, ref, "t", []TranscriptMessage{userMsg("hi")}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	for _, rec := range got {
		if rec.UserId != "alice" {
			t.Errorf("session %s user = %q, want alice", rec.Id, rec.UserId)
		}
		if rec.Channel != "telegram" {
			t.Errorf("session %s channel = %q, want telegram", rec.Id, rec.Channel)
		}
	}
}

func TestSessionAppendOnFollowerReturnsErrNotLeader(t *testing.T) {
	t.Parallel()
	svc := newTestServiceStack(t)
	// No raft wired = can't be the leader; the gateway relies on
	// errors.Is here to decide whether to degrade to its cache.
	s := NewSessionService(nil, svc.store, SessionConfig{})
	_, err := s.Append(context.Background(),
		SessionRef{Channel: "rest", ChannelID: "1"}, "t",
		[]TranscriptMessage{userMsg("x")})
	if !errors.Is(err, ErrNotLeader) {
		t.Errorf("got %v, want ErrNotLeader", err)
	}
}
