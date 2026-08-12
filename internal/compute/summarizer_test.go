package compute

import (
	"context"
	"strings"
	"testing"
)

func TestSummarizerSendsPriorSummaryAndMessages(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(MockResponse{Content: "the user is called james"})
	s := NewLLMSummarizer(provider, "test-model")

	got, err := s.SummarizeConversation(context.Background(), "earlier: they said hello",
		[]Message{{Role: "user", Content: "my name is james"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "the user is called james" {
		t.Errorf("summary = %q", got)
	}
	calls := provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	prompt := calls[0].Messages[len(calls[0].Messages)-1].Content
	if !strings.Contains(prompt, "earlier: they said hello") {
		t.Error("prior summary not carried into the prompt")
	}
	if !strings.Contains(prompt, "my name is james") {
		t.Error("new messages not carried into the prompt")
	}
}

func TestSummarizerNoPriorReadsAsConversationStart(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(MockResponse{Content: "ok"})
	s := NewLLMSummarizer(provider, "m")
	if _, err := s.SummarizeConversation(context.Background(), "", []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	prompt := provider.Calls()[0].Messages[1].Content
	if !strings.Contains(prompt, "no summary yet") {
		t.Errorf("first compaction should say so: %q", prompt[:80])
	}
}

// The summariser exists to save tokens; shipping it 10 MB of grep
// output would defeat that on the very call meant to help.
func TestSummarizerTruncatesToolResults(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(MockResponse{Content: "ok"})
	s := NewLLMSummarizer(provider, "m")
	huge := strings.Repeat("match\n", 5000)

	if _, err := s.SummarizeConversation(context.Background(), "", []Message{
		{Role: "tool", ToolCallID: "c1", Content: huge},
	}); err != nil {
		t.Fatal(err)
	}
	prompt := provider.Calls()[0].Messages[1].Content
	if len(prompt) > 2000 {
		t.Errorf("prompt is %d bytes; tool output should have been truncated", len(prompt))
	}
	if !strings.Contains(prompt, "bytes total") {
		t.Error("truncation should say how much was dropped")
	}
}

func TestSummarizerNamesToolCalls(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(MockResponse{Content: "ok"})
	s := NewLLMSummarizer(provider, "m")
	if _, err := s.SummarizeConversation(context.Background(), "", []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "shell_command"}, {Name: "read_file"}}},
	}); err != nil {
		t.Fatal(err)
	}
	prompt := provider.Calls()[0].Messages[1].Content
	if !strings.Contains(prompt, "shell_command") || !strings.Contains(prompt, "read_file") {
		t.Errorf("tool names should survive into the summary prompt: %q", prompt)
	}
}

func TestSummarizerEmptyBatchReturnsPriorUnchanged(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider()
	s := NewLLMSummarizer(provider, "m")
	got, err := s.SummarizeConversation(context.Background(), "unchanged", nil)
	if err != nil || got != "unchanged" {
		t.Errorf("got %q, %v; want the prior summary and no provider call", got, err)
	}
	if provider.CallCount() != 0 {
		t.Error("empty batch should not cost an LLM call")
	}
}

func TestNewLLMSummarizerNilWithoutProvider(t *testing.T) {
	t.Parallel()
	if s := NewLLMSummarizer(nil, "m"); s != nil {
		t.Error("no provider should mean no summariser (compaction off)")
	}
}
