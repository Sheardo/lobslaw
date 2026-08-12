package compute

import (
	"strings"
	"testing"
)

func msgs(n int, role, content string) []Message {
	out := make([]Message, 0, n)
	for range n {
		out = append(out, Message{Role: role, Content: content})
	}
	return out
}

func TestContextBudgetZeroValueIsPassThrough(t *testing.T) {
	t.Parallel()
	in := []Message{
		{Role: "user", Content: strings.Repeat("x", 100000)},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("y", 100000)},
	}
	// The orphan sweep still runs — an unpaired tool result is
	// invalid on the wire regardless of budget.
	got := ContextBudget{}.Apply(in)
	if len(got) != 1 || got[0].Role != "user" {
		t.Errorf("got %d messages %+v; want just the user message", len(got), got)
	}
}

func TestElideTruncatesReplayedToolResults(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("z", 5000)
	in := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "grep"}}},
		{Role: "tool", ToolCallID: "c1", Content: big},
	}
	got := ContextBudget{HistoryToolResultBytes: 512}.Apply(in)
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if len(got[1].Content) >= len(big) {
		t.Errorf("tool result not elided: %d bytes", len(got[1].Content))
	}
	if !strings.Contains(got[1].Content, "bytes elided") {
		t.Errorf("elision marker missing: %q", got[1].Content[len(got[1].Content)-80:])
	}
	if !strings.HasPrefix(got[1].Content, strings.Repeat("z", 512)) {
		t.Error("elided result should keep the head of the output")
	}
}

func TestElideLeavesInputUnmutated(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("z", 5000)
	in := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1"}}},
		{Role: "tool", ToolCallID: "c1", Content: big},
	}
	_ = ContextBudget{HistoryToolResultBytes: 100}.Apply(in)
	if len(in[1].Content) != 5000 {
		t.Errorf("Apply mutated its input: tool content now %d bytes", len(in[1].Content))
	}
}

func TestElideLeavesUserAndAssistantTextAlone(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 5000)
	in := []Message{
		{Role: "user", Content: long},
		{Role: "assistant", Content: long},
	}
	got := ContextBudget{HistoryToolResultBytes: 10}.Apply(in)
	for i, m := range got {
		if len(m.Content) != 5000 {
			t.Errorf("message %d (%s) was truncated to %d bytes; only tool results should be", i, m.Role, len(m.Content))
		}
	}
}

func TestTrimKeepsNewestWithinBudget(t *testing.T) {
	t.Parallel()
	// Each message ~25 estimated tokens (100 bytes / 4 + overhead).
	in := make([]Message, 0, 10)
	for i := range 10 {
		in = append(in, Message{Role: "user", Content: strings.Repeat("x", 100) + string(rune('a'+i))})
	}
	got := ContextBudget{TailTokens: 100}.Apply(in)
	if len(got) == 0 || len(got) >= 10 {
		t.Fatalf("got %d messages; want a trimmed subset of 10", len(got))
	}
	// Whatever survived must be the tail, ending at the newest.
	last := got[len(got)-1]
	if last.Content != in[9].Content {
		t.Errorf("trim kept the wrong end — last message is not the newest")
	}
}

func TestTrimKeepsOneOversizeMessageRatherThanNothing(t *testing.T) {
	t.Parallel()
	in := []Message{{Role: "user", Content: strings.Repeat("x", 100000)}}
	got := ContextBudget{TailTokens: 10}.Apply(in)
	if len(got) != 1 {
		t.Errorf("got %d messages; a single over-budget message should still be kept", len(got))
	}
}

func TestTrimNoOpWhenUnderBudget(t *testing.T) {
	t.Parallel()
	in := msgs(3, "user", "short")
	got := ContextBudget{TailTokens: 100000}.Apply(in)
	if len(got) != 3 {
		t.Errorf("got %d messages, want 3 untouched", len(got))
	}
}

// The trap: trimming from the oldest end routinely cuts an assistant
// message while keeping the tool results that answered it. Providers
// reject a tool_call_id no preceding message claims, which would turn
// every later turn in that conversation into a 400.
func TestTrimNeverLeavesOrphanedToolResults(t *testing.T) {
	t.Parallel()
	filler := strings.Repeat("x", 400)
	in := []Message{
		{Role: "user", Content: "old question " + filler},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "shell_command"}}},
		{Role: "tool", ToolCallID: "c1", Content: "old result " + filler},
		{Role: "assistant", Content: "old answer " + filler},
		{Role: "user", Content: "new question"},
		{Role: "assistant", Content: "new answer"},
	}
	got := ContextBudget{TailTokens: 60}.Apply(in)

	claimed := map[string]bool{}
	for _, m := range got {
		for _, tc := range m.ToolCalls {
			claimed[tc.ID] = true
		}
		if m.Role == "tool" && !claimed[m.ToolCallID] {
			t.Fatalf("orphaned tool result %q survived trimming: %+v", m.ToolCallID, got)
		}
	}
}

func TestOrphanSweepPreservesEarlierValidMessages(t *testing.T) {
	t.Parallel()
	// A valid pair, then an orphan, then more valid messages. The
	// sweep must keep everything except the orphan.
	in := []Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1"}}},
		{Role: "tool", ToolCallID: "c1", Content: "r1"},
		{Role: "tool", ToolCallID: "GHOST", Content: "orphan"},
		{Role: "assistant", Content: "a1"},
	}
	got := ContextBudget{}.Apply(in)
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4 (all but the orphan): %+v", len(got), got)
	}
	for _, m := range got {
		if m.ToolCallID == "GHOST" {
			t.Error("orphan survived")
		}
	}
	if got[0].Content != "q1" || got[3].Content != "a1" {
		t.Errorf("valid messages around the orphan were lost: %+v", got)
	}
}

func TestPairedToolResultsSurviveWhenWholeTurnFits(t *testing.T) {
	t.Parallel()
	in := []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "grep"}}},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{Role: "assistant", Content: "a"},
	}
	got := ContextBudget{TailTokens: 10000, HistoryToolResultBytes: 512}.Apply(in)
	if len(got) != 4 {
		t.Fatalf("got %d messages, want all 4 kept: %+v", len(got), got)
	}
}

func TestEmptyHistory(t *testing.T) {
	t.Parallel()
	if got := DefaultContextBudget().Apply(nil); len(got) != 0 {
		t.Errorf("got %d messages from nil history", len(got))
	}
}

// Elision alone should often save enough that no message has to be
// dropped — the shape of the conversation is worth more than the
// bytes it lost.
func TestElisionCanSaveMessagesFromBeingDropped(t *testing.T) {
	t.Parallel()
	in := []Message{
		{Role: "user", Content: "what's in the repo?"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "grep"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("match\n", 4000)},
		{Role: "assistant", Content: "lots of matches"},
	}
	withoutElision := ContextBudget{TailTokens: 500}.Apply(in)
	withElision := ContextBudget{TailTokens: 500, HistoryToolResultBytes: 512}.Apply(in)

	if len(withElision) <= len(withoutElision) {
		t.Errorf("elision kept %d messages, no better than %d without it",
			len(withElision), len(withoutElision))
	}
	if len(withElision) != 4 {
		t.Errorf("got %d messages; elision should have kept the whole exchange", len(withElision))
	}
}

func TestEstimateTokensCountsToolCallArguments(t *testing.T) {
	t.Parallel()
	bare := estimateTokens(Message{Role: "assistant"})
	withCall := estimateTokens(Message{
		Role:      "assistant",
		ToolCalls: []ToolCall{{ID: "c1", Name: "shell_command", Arguments: strings.Repeat("x", 400)}},
	})
	if withCall <= bare+50 {
		t.Errorf("tool call arguments barely counted: bare=%d withCall=%d", bare, withCall)
	}
}

// The integration trap: once the agent trims history, a caller that
// derives the turn boundary itself ("system + the history I passed
// in") lands past the end of the message list and persists nothing.
// TurnStartIndex has to point at this turn's own first message no
// matter how much history was dropped.
func TestTurnStartIndexSurvivesHistoryTrimming(t *testing.T) {
	t.Parallel()
	agent, err := NewAgent(AgentConfig{
		Provider:      NewMockProvider(MockResponse{Content: "reply"}),
		ContextBudget: ContextBudget{TailTokens: 50},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Far more history than the budget allows.
	var history []Message
	for i := range 40 {
		history = append(history,
			Message{Role: "user", Content: strings.Repeat("x", 200) + string(rune('a'+i%26))},
			Message{Role: "assistant", Content: strings.Repeat("y", 200)})
	}

	budget, _ := NewTurnBudget(BudgetCaps{})
	resp, err := agent.RunToolCallLoop(t.Context(), ProcessMessageRequest{
		Message:             "the new question",
		SystemPrompt:        "sys",
		Budget:              budget,
		ConversationHistory: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.TurnStartIndex >= len(resp.Messages) {
		t.Fatalf("TurnStartIndex %d is past the end of %d messages — the caller would persist nothing",
			resp.TurnStartIndex, len(resp.Messages))
	}
	newTurn := resp.Messages[resp.TurnStartIndex:]
	if len(newTurn) != 2 {
		t.Fatalf("turn slice = %d messages, want 2 (user + assistant): %+v", len(newTurn), newTurn)
	}
	if newTurn[0].Role != "user" || newTurn[0].Content != "the new question" {
		t.Errorf("turn does not start at this turn's user message: %+v", newTurn[0])
	}
	if newTurn[1].Role != "assistant" || newTurn[1].Content != "reply" {
		t.Errorf("turn does not end with this turn's reply: %+v", newTurn[1])
	}
	// And trimming must actually have happened, else this proves nothing.
	if len(resp.Messages) >= len(history) {
		t.Errorf("history was not trimmed (%d messages sent, %d in history)", len(resp.Messages), len(history))
	}
}

func TestTurnStartIndexWithoutTrimming(t *testing.T) {
	t.Parallel()
	agent, err := NewAgent(AgentConfig{Provider: NewMockProvider(MockResponse{Content: "reply"})})
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := NewTurnBudget(BudgetCaps{})
	resp, err := agent.RunToolCallLoop(t.Context(), ProcessMessageRequest{
		Message:      "hello",
		SystemPrompt: "sys",
		Budget:       budget,
		ConversationHistory: []Message{
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "older reply"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	newTurn := resp.Messages[resp.TurnStartIndex:]
	if len(newTurn) != 2 || newTurn[0].Content != "hello" {
		t.Errorf("turn slice = %+v; want [hello, reply]", newTurn)
	}
}
