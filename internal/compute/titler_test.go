package compute

import (
	"context"
	"strings"
	"testing"
)

// Models add what they're told not to: quotes, a "Title:" prefix,
// trailing full stops, a second line of explanation.
func TestCleanTitleStripsModelHabits(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`"Raft snapshot corruption"`, "Raft snapshot corruption"},
		{"Title: Deploy pipeline redesign", "Deploy pipeline redesign"},
		{"Session about caching.", "Session about caching"},
		{"First line\nSecond line explaining it", "First line"},
		{"  padded  ", "padded"},
		{"`backticked`", "backticked"},
	}
	for _, c := range cases {
		if got := cleanTitle(c.in, 60); got != c.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanTitleTruncatesAtWordBoundary(t *testing.T) {
	t.Parallel()
	long := "an extremely long conversation title that rambles well past any sensible limit"
	got := cleanTitle(long, 30)
	if len(got) > 32 {
		t.Errorf("title is %d chars: %q", len(got), got)
	}
	if strings.Contains(got, "  ") || strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("ragged truncation: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated title should be marked: %q", got)
	}
}

func TestTitlerReturnsEmptyForEmptySummary(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider()
	tl := NewLLMTitler(provider, "m", 60)
	got, err := tl.Title(context.Background(), "   ")
	if err != nil || got != "" {
		t.Errorf("got %q, %v; want empty and no provider call", got, err)
	}
	if provider.CallCount() != 0 {
		t.Error("empty summary should not cost an LLM call")
	}
}

func TestTitlerUsesTheSummary(t *testing.T) {
	t.Parallel()
	provider := NewMockProvider(MockResponse{Content: "Raft snapshot corruption"})
	tl := NewLLMTitler(provider, "m", 60)
	got, err := tl.Title(context.Background(), "the user debugged a corrupt raft snapshot on node B")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Raft snapshot corruption" {
		t.Errorf("title = %q", got)
	}
	if !strings.Contains(provider.Calls()[0].Messages[1].Content, "node B") {
		t.Error("summary was not passed to the titler")
	}
}

func TestNewLLMTitlerNilWithoutProvider(t *testing.T) {
	t.Parallel()
	if tl := NewLLMTitler(nil, "m", 60); tl != nil {
		t.Error("no provider should mean no titler")
	}
}
