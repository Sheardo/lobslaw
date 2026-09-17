package compute

import (
	"strings"
	"testing"
)

func sse(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: " + f + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// Text arrives in pieces and must come back whole, in order, and be
// handed out as it goes.
func TestStreamAssemblesTextAndEmitsItAsItArrives(t *testing.T) {
	t.Parallel()

	body := sse(
		`{"choices":[{"delta":{"content":"The cluster "}}]}`,
		`{"choices":[{"delta":{"content":"is healthy"}}]}`,
		`{"choices":[{"delta":{"content":"."},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
	)

	var seen []string
	resp, usage, err := readChatStream(strings.NewReader(body), func(s string) { seen = append(seen, s) })
	if err != nil {
		t.Fatalf("readChatStream: %v", err)
	}
	if resp.Content != "The cluster is healthy." {
		t.Errorf("Content = %q", resp.Content)
	}
	if len(seen) != 3 {
		t.Errorf("emitted %d deltas, want one per chunk — buffering defeats the point", len(seen))
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	// Usage only arrives because stream_options.include_usage is set.
	// Without it every streamed turn would silently cost zero while
	// the budget carried on looking like it was counting.
	if usage.TotalTokens != 14 {
		t.Errorf("TotalTokens = %d, want 14 — usage was lost", usage.TotalTokens)
	}
}

// Tool calls are the part that breaks quietly.
//
// The name arrives in the first fragment and the arguments dribble
// across the rest, so a reassembler that appends instead of keying by
// index produces JSON that fails to parse with no clue why. Two
// interleaved calls is the case that catches it.
func TestStreamReassemblesInterleavedToolCalls(t *testing.T) {
	t.Parallel()

	body := sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"inbox_post","arguments":"{\"bot_id\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"notify","arguments":"{\"text\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"devops\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":":\"done\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	resp, _, err := readChatStream(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("readChatStream: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	// Index order, not arrival order.
	if resp.ToolCalls[0].Name != "inbox_post" || resp.ToolCalls[1].Name != "notify" {
		t.Errorf("calls out of index order: %+v", resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Arguments; got != `{"bot_id":"devops"}` {
		t.Errorf("call 0 arguments = %q — fragments were not joined correctly", got)
	}
	if got := resp.ToolCalls[1].Arguments; got != `{"text":"done"}` {
		t.Errorf("call 1 arguments = %q", got)
	}
}

// Providers emit keepalives and vendor frames that are none of our
// business. One unparseable frame must not discard a good completion.
func TestStreamSurvivesFramesItDoesNotUnderstand(t *testing.T) {
	t.Parallel()

	body := "data: {not json at all}\n\n" +
		": this is an SSE comment\n\n" +
		`data: {"choices":[{"delta":{"content":"still here"}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	resp, _, err := readChatStream(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("readChatStream: %v", err)
	}
	if resp.Content != "still here" {
		t.Errorf("Content = %q, want the frame that did parse", resp.Content)
	}
}

// A nil hook is the normal case — most turns have no audience.
func TestStreamWithNoObserverStillAssembles(t *testing.T) {
	t.Parallel()

	resp, _, err := readChatStream(
		strings.NewReader(sse(`{"choices":[{"delta":{"content":"quiet"}}]}`)), nil)
	if err != nil {
		t.Fatalf("readChatStream: %v", err)
	}
	if resp.Content != "quiet" {
		t.Errorf("Content = %q", resp.Content)
	}
}
