package compute

import (
	"sync"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/trace"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A tool's cost is NOT the call that ran it. It is the tokens its
// output contributes to every subsequent prompt in the turn. A tool
// returning 8k tokens on the first of six model calls is billed five
// more times, at the prompt rate — usually the dominant cost in an
// agentic turn, and attributable to nothing today.

type capturingSink struct {
	mu   sync.Mutex
	got  []trace.Span
	done chan struct{}
	want int
}

func newCapturingSink(want int) *capturingSink {
	return &capturingSink{done: make(chan struct{}), want: want}
}

func (c *capturingSink) Write(s trace.Span) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, s)
	if c.want > 0 && len(c.got) >= c.want {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return nil
}
func (c *capturingSink) Close() error { return nil }

func (c *capturingSink) spans() []trace.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]trace.Span(nil), c.got...)
}

func (c *capturingSink) carries() []trace.Span {
	var out []trace.Span
	for _, s := range c.spans() {
		if s.Kind == trace.KindContextCarry {
			out = append(out, s)
		}
	}
	return out
}

// attributor builds one wired to a capturing sink.
func attributor(t *testing.T) (*toolAttributor, *capturingSink, *trace.Recorder) {
	t.Helper()
	sink := newCapturingSink(0)
	rec := trace.NewRecorder(nil, sink)
	t.Cleanup(func() { _ = rec.Close() })
	return newToolAttributor(rec, "turn-1"), sink, rec
}

func toolResult(name, output string) ToolInvocation {
	return ToolInvocation{ToolName: name, Output: output}
}

// The core arithmetic. A result produced by call 1 of 3 rides in the
// prompts of calls 2 and 3 — two carries, not zero and not three.
func TestAToolIsChargedForEveryLaterPrompt(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	pricing := types.ProviderPricing{InputUSDPer1K: 1.0}

	a.noteLLMCall(pricing) // call 1 asks for the tool
	a.noteTool(toolResult("search", "x"), 0, time.Now())
	a.noteLLMCall(pricing) // call 2 carries it
	a.noteLLMCall(pricing) // call 3 carries it
	a.flush()
	_ = rec.Close()

	got := sink.carries()
	if len(got) != 1 {
		t.Fatalf("got %d carry spans", len(got))
	}
	if got[0].Attempt != 2 {
		t.Errorf("carries = %d, want 2 (calls 2 and 3)", got[0].Attempt)
	}
}

// A tool called on the FINAL round-trip is paid for once, in the
// reply, and never re-sent. Zero is a real and useful answer.
func TestAToolOnTheLastCallCarriesNothing(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)

	a.noteLLMCall(types.ProviderPricing{InputUSDPer1K: 1.0})
	a.noteTool(toolResult("search", "x"), 0, time.Now())
	a.flush()
	_ = rec.Close()

	got := sink.carries()
	// Emitted, not omitted: "this tool was free" must be
	// distinguishable from "this tool was not recorded".
	if len(got) != 1 {
		t.Fatalf("a zero-carry tool produced %d spans; it should produce one", len(got))
	}
	if got[0].Attempt != 0 || got[0].CostUSD != 0 {
		t.Errorf("carries = %d, cost = %v; want zero of each", got[0].Attempt, got[0].CostUSD)
	}
}

// The design's own example: 8k tokens on the first of six calls is
// carried in five later prompts, so it costs roughly 40k prompt
// tokens — not 8k.
func TestTheDominantCostIsTheCarryNotTheCall(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	// A realistic input rate, so the resulting figure is one somebody
	// would recognise from a bill.
	pricing := types.ProviderPricing{InputUSDPer1K: 0.003}

	// ~8k tokens at the chars/4 estimate.
	big := make([]byte, 32000)
	for i := range big {
		big[i] = 'x'
	}

	a.noteLLMCall(pricing)
	a.noteTool(toolResult("fetch", string(big)), 0, time.Now())
	for range 5 {
		a.noteLLMCall(pricing)
	}
	a.flush()
	_ = rec.Close()

	got := sink.carries()
	if len(got) != 1 {
		t.Fatalf("got %d carry spans", len(got))
	}
	if got[0].Attempt != 5 {
		t.Fatalf("carries = %d, want 5", got[0].Attempt)
	}
	// Five carries of ~8k, so ~40k prompt tokens — several times the
	// 8k the tool call itself looks like.
	if got[0].Usage.PromptTokens < 35000 || got[0].Usage.PromptTokens > 45000 {
		t.Errorf("carried tokens = %d, want ~40k", got[0].Usage.PromptTokens)
	}
	// 40k tokens at $0.003/1k is ~$0.12 — against a tool call that
	// looks like an 8k event, i.e. ~$0.024. The carry is five times
	// the thing anybody would have attributed it to.
	if got[0].CostUSD < 0.10 || got[0].CostUSD > 0.14 {
		t.Errorf("cost = %v, want ~0.12", got[0].CostUSD)
	}
}

// Priced at the PROMPT rate. Re-sent context is input tokens on every
// subsequent call; using the completion rate would overstate it
// several-fold.
func TestTheCarryIsPricedAsInputNotOutput(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	// Output priced 10x input: if the carry used the wrong rate the
	// number would be an order of magnitude out.
	a.noteLLMCall(types.ProviderPricing{InputUSDPer1K: 1.0, OutputUSDPer1K: 10.0})
	a.noteTool(toolResult("search", "0123456789012345678901234567890123456789"), 0, time.Now())
	a.noteLLMCall(types.ProviderPricing{InputUSDPer1K: 1.0, OutputUSDPer1K: 10.0})
	a.flush()
	_ = rec.Close()

	got := sink.carries()
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	want := EstimateCost(Usage{PromptTokens: got[0].Usage.PromptTokens},
		types.ProviderPricing{InputUSDPer1K: 1.0})
	if got[0].CostUSD != want {
		t.Errorf("cost = %v, want %v (the input rate)", got[0].CostUSD, want)
	}
}

// Several tools in one turn are charged independently, by when each
// entered the message list.
func TestEachToolIsChargedFromWhenItEntered(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	pricing := types.ProviderPricing{InputUSDPer1K: 1.0}

	a.noteLLMCall(pricing)
	a.noteTool(toolResult("early", "x"), 0, time.Now())
	a.noteLLMCall(pricing)
	a.noteTool(toolResult("late", "x"), 0, time.Now())
	a.noteLLMCall(pricing)
	a.flush()
	_ = rec.Close()

	byName := map[string]int{}
	for _, s := range sink.carries() {
		byName[s.Name] = s.Attempt
	}
	if byName["early"] != 2 {
		t.Errorf("early carried %d times, want 2", byName["early"])
	}
	if byName["late"] != 1 {
		t.Errorf("late carried %d times, want 1", byName["late"])
	}
}

// --- the tool span itself -------------------------------------------

// Emitted immediately, because a turn that times out or blows its
// budget is exactly the turn somebody wants the trace of, and
// buffering would lose it on every path that matters.
func TestTheToolSpanIsEmittedBeforeTheTurnEnds(t *testing.T) {
	t.Parallel()
	sink := newCapturingSink(1)
	rec := trace.NewRecorder(nil, sink)
	a := newToolAttributor(rec, "turn-1")

	a.noteTool(toolResult("search", "hello"), 12*time.Millisecond, time.Now())
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool span was buffered until flush")
	}
	_ = rec.Close()

	var tool *trace.Span
	for _, s := range sink.spans() {
		if s.Kind == trace.KindToolCall {
			tool = &s
		}
	}
	if tool == nil {
		t.Fatal("no tool_call span")
	}
	if tool.Name != "search" || tool.Duration != 12*time.Millisecond {
		t.Errorf("span = %+v", *tool)
	}
	if tool.ResultSize != len("hello") {
		t.Errorf("result bytes = %d", tool.ResultSize)
	}
}

// A failing tool is not retried against another provider, so nothing
// "advanced" — aborted is the honest outcome.
func TestAFailingToolIsAborted(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	a.noteTool(ToolInvocation{ToolName: "shell", Error: "exit 1"}, 0, time.Now())
	a.flush()
	_ = rec.Close()

	for _, s := range sink.spans() {
		if s.Kind == trace.KindToolCall && s.Outcome != trace.OutcomeAborted {
			t.Errorf("outcome = %s, want aborted", s.Outcome)
		}
	}
}

// The carry span parents to the tool span, so a collector shows the
// cost underneath the thing that caused it.
func TestTheCarryParentsToItsTool(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	a.noteLLMCall(types.ProviderPricing{})
	a.noteTool(toolResult("search", "x"), 0, time.Now())
	a.noteLLMCall(types.ProviderPricing{})
	a.flush()
	_ = rec.Close()

	var toolSpanID string
	for _, s := range sink.spans() {
		if s.Kind == trace.KindToolCall {
			toolSpanID = s.SpanID
		}
	}
	carries := sink.carries()
	if len(carries) != 1 {
		t.Fatalf("got %d carries", len(carries))
	}
	if carries[0].ParentID != toolSpanID {
		t.Errorf("carry parent = %q, tool span = %q", carries[0].ParentID, toolSpanID)
	}
}

// --- absence ---------------------------------------------------------

// With tracing off there is no attributor, and every method tolerates
// nil so the agent loop calls them unconditionally.
func TestANilAttributorIsInert(t *testing.T) {
	t.Parallel()
	if a := newToolAttributor(nil, "turn-1"); a != nil {
		t.Fatal("an attributor was built with no recorder")
	}
	var a *toolAttributor
	a.noteLLMCall(types.ProviderPricing{})
	a.noteTool(toolResult("search", "x"), 0, time.Now())
	a.flush()
}

// flush is deferred on every exit path, and a turn can end twice on
// paths that both return — so a second flush must not double-charge.
func TestFlushingTwiceDoesNotDoubleCharge(t *testing.T) {
	t.Parallel()
	a, sink, rec := attributor(t)
	a.noteLLMCall(types.ProviderPricing{InputUSDPer1K: 1.0})
	a.noteTool(toolResult("search", "x"), 0, time.Now())
	a.noteLLMCall(types.ProviderPricing{InputUSDPer1K: 1.0})
	a.flush()
	a.flush()
	_ = rec.Close()

	if got := len(sink.carries()); got != 1 {
		t.Errorf("got %d carry spans after two flushes", got)
	}
}
