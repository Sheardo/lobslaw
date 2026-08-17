package compute

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A chain's later steps run as a pipeline: step N's output is rendered
// into step N+1's template, and the last step's output is the answer.
//
// The failure behaviour matters more than the happy path. By the time
// these run, the user already has a complete reply from step 0 —
// losing it because a reviewer's provider was rate-limited would make
// a chain a liability rather than an improvement.

// echoProvider returns whatever it was prompted with, tagged, so a
// test can see which provider handled a step and what it was asked.
type echoProvider struct {
	label   string
	err     error
	reply   string
	prompts *[]string
}

func (p *echoProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	if p.prompts != nil && len(req.Messages) > 0 {
		*p.prompts = append(*p.prompts, req.Messages[len(req.Messages)-1].Content)
	}
	if p.err != nil {
		return nil, p.err
	}
	reply := p.reply
	if reply == "" {
		reply = "reviewed by " + p.label
	}
	return &ChatResponse{Content: reply, FinishReason: "stop"}, nil
}

// stepAgent wires a two-step chain: "drafter" then "reviewer".
func stepAgent(t *testing.T, reviewer *echoProvider, tmpl string) (*Agent, context.Context) {
	t.Helper()
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{
		Label: "drafter", TrustTier: types.TrustPrivate,
		Client: &echoProvider{label: "drafter", reply: "the draft"},
	})
	reviewer.label = "reviewer"
	reg.Register(ProviderEntry{
		Label: "reviewer", Model: "rev-model", TrustTier: types.TrustPrivate,
		Client: reviewer,
	})
	a := &Agent{cfg: AgentConfig{
		Provider:     &echoProvider{label: "unused"},
		Providers:    reg,
		PrimaryLabel: "drafter",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	route := &Route{
		StartLabel: "drafter",
		ChainLabel: "review",
		Steps: []ResolveStep{
			{Provider: config.ProviderConfig{Label: "drafter"}, Role: "primary"},
			{Provider: config.ProviderConfig{Label: "reviewer", Model: "rev-model"},
				Role: "reviewer", PromptTemplate: tmpl},
		},
	}
	return a, WithRoute(context.Background(), route)
}

func turnReq(t *testing.T) ProcessMessageRequest {
	t.Helper()
	budget, err := NewTurnBudget(BudgetCaps{})
	if err != nil {
		t.Fatal(err)
	}
	return ProcessMessageRequest{Message: "what is a raft snapshot?", Budget: budget}
}

// The last step's output is the answer.
func TestTheLastStepsOutputIsTheAnswer(t *testing.T) {
	t.Parallel()
	a, ctx := stepAgent(t, &echoProvider{}, "")
	got := a.runChainSteps(ctx, turnReq(t), "the draft")
	if got != "reviewed by reviewer" {
		t.Errorf("answer = %q; the reviewer's output did not become the answer", got)
	}
}

// The step sees the draft AND the question. A reviewer that cannot see
// what was asked can only check the answer against itself.
func TestAStepSeesThePreviousOutputAndTheQuestion(t *testing.T) {
	t.Parallel()
	prompts := &[]string{}
	a, ctx := stepAgent(t, &echoProvider{prompts: prompts}, "")
	a.runChainSteps(ctx, turnReq(t), "the draft")

	if len(*prompts) != 1 {
		t.Fatalf("prompts = %v", *prompts)
	}
	if !strings.Contains((*prompts)[0], "the draft") {
		t.Error("the step was not shown the previous output")
	}
	if !strings.Contains((*prompts)[0], "what is a raft snapshot?") {
		t.Error("the step was not shown the user's question")
	}
}

func TestAStepTemplateIsRendered(t *testing.T) {
	t.Parallel()
	prompts := &[]string{}
	a, ctx := stepAgent(t, &echoProvider{prompts: prompts},
		"Shorten this: {{.Previous}} (asked: {{.Message}})")
	a.runChainSteps(ctx, turnReq(t), "a long draft")

	want := "Shorten this: a long draft (asked: what is a raft snapshot?)"
	if len(*prompts) != 1 || (*prompts)[0] != want {
		t.Errorf("prompt = %v, want %q", *prompts, want)
	}
}

// --- failure keeps the best answer so far --------------------------

// The user already has a complete reply. Losing it because a
// reviewer's provider was rate-limited would make the chain a
// liability.
func TestAFailingStepKeepsThePreviousAnswer(t *testing.T) {
	t.Parallel()
	a, ctx := stepAgent(t, &echoProvider{err: errors.New("429 slow down")}, "")
	got := a.runChainSteps(ctx, turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q; a failed step lost the answer", got)
	}
}

// An empty reply is a failure that did not announce itself. Returning
// it would replace a good answer with silence.
func TestAnEmptyStepReplyKeepsThePreviousAnswer(t *testing.T) {
	t.Parallel()
	a, ctx := stepAgent(t, &echoProvider{reply: "   "}, "")
	got := a.runChainSteps(ctx, turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q; an empty step reply replaced the answer", got)
	}
}

// A template the operator got wrong must not silently become
// different instructions — the reply would be one they did not ask for
// and cannot account for.
func TestABrokenTemplateKeepsThePreviousAnswer(t *testing.T) {
	t.Parallel()
	a, ctx := stepAgent(t, &echoProvider{}, "{{.Previous")
	got := a.runChainSteps(ctx, turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q; a broken template should not change the answer", got)
	}
}

// A template naming a field that does not exist is the same case.
func TestATemplateWithAnUnknownFieldKeepsThePreviousAnswer(t *testing.T) {
	t.Parallel()
	a, ctx := stepAgent(t, &echoProvider{}, "{{.Nonexistent}}")
	got := a.runChainSteps(ctx, turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q", got)
	}
}

// --- the ordinary case is no case at all ---------------------------

func TestASingleStepChainRunsNothingExtra(t *testing.T) {
	t.Parallel()
	prompts := &[]string{}
	a, _ := stepAgent(t, &echoProvider{prompts: prompts}, "")
	route := &Route{StartLabel: "drafter", Steps: []ResolveStep{
		{Provider: config.ProviderConfig{Label: "drafter"}, Role: "primary"},
	}}
	got := a.runChainSteps(WithRoute(context.Background(), route), turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q", got)
	}
	if len(*prompts) != 0 {
		t.Errorf("a single-step chain called a second provider: %v", *prompts)
	}
}

// No route at all is every turn that predates chains routing.
func TestNoRouteRunsNothingExtra(t *testing.T) {
	t.Parallel()
	prompts := &[]string{}
	a, _ := stepAgent(t, &echoProvider{prompts: prompts}, "")
	got := a.runChainSteps(context.Background(), turnReq(t), "the draft")
	if got != "the draft" {
		t.Errorf("answer = %q", got)
	}
	if len(*prompts) != 0 {
		t.Errorf("a turn with no route called a second provider: %v", *prompts)
	}
}

// --- what a step is NOT given --------------------------------------

// A step refines what the previous one said. Tools would reopen the
// whole tool-call loop once per step, and a chain of three would be
// three agents rather than one answer passed along.
func TestAStepGetsNoTools(t *testing.T) {
	t.Parallel()
	var seen ChatRequest
	captor := &toolCaptor{seen: &seen}
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{Label: "drafter", TrustTier: types.TrustPrivate,
		Client: &echoProvider{label: "drafter"}})
	reg.Register(ProviderEntry{Label: "reviewer", TrustTier: types.TrustPrivate, Client: captor})
	a := &Agent{cfg: AgentConfig{
		Provider: &echoProvider{label: "unused"}, Providers: reg,
		PrimaryLabel: "drafter",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	ctx := WithRoute(context.Background(), &Route{StartLabel: "drafter", Steps: []ResolveStep{
		{Provider: config.ProviderConfig{Label: "drafter"}},
		{Provider: config.ProviderConfig{Label: "reviewer"}},
	}})

	req := turnReq(t)
	req.Tools = []Tool{{Name: "read_file"}}
	a.runChainSteps(ctx, req, "the draft")

	if len(seen.Tools) != 0 {
		t.Errorf("the step was advertised %d tools", len(seen.Tools))
	}
}

type toolCaptor struct{ seen *ChatRequest }

func (c *toolCaptor) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	*c.seen = req
	return &ChatResponse{Content: "reviewed", FinishReason: "stop"}, nil
}

// --- the turn actually runs the pipeline ---------------------------

// Everything above tests runChainSteps directly. This tests that the
// TURN calls it — that a two-step chain changes what the user is
// handed, not just what a helper returns.
//
// Added after a mutation replacing the loop's call with step 0's draft
// failed nothing at all.
func TestATwoStepChainChangesWhatTheUserIsHanded(t *testing.T) {
	t.Parallel()
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{
		Label: "drafter", TrustTier: types.TrustPrivate,
		Client: NewMockProvider(MockResponse{Content: "the draft"}),
	})
	reg.Register(ProviderEntry{
		Label: "reviewer", TrustTier: types.TrustPrivate,
		Client: NewMockProvider(MockResponse{Content: "the reviewed reply"}),
	})
	a, err := NewAgent(AgentConfig{
		Provider:     NewMockProvider(MockResponse{Content: "unused"}),
		Providers:    reg,
		PrimaryLabel: "drafter",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithRoute(context.Background(), &Route{
		StartLabel: "drafter", ChainLabel: "review",
		Steps: []ResolveStep{
			{Provider: config.ProviderConfig{Label: "drafter"}, Role: "primary"},
			{Provider: config.ProviderConfig{Label: "reviewer"}, Role: "reviewer"},
		},
	})
	budget, _ := NewTurnBudget(BudgetCaps{})
	resp, err := a.RunToolCallLoop(ctx, ProcessMessageRequest{
		Message: "what is a raft snapshot?",
		Claims:  &types.Claims{UserID: "alice"},
		Budget:  budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply != "the reviewed reply" {
		t.Errorf("reply = %q; the turn handed back step 0's draft", resp.Reply)
	}
}

// A single-step turn must be untouched — this is every turn that does
// not configure a multi-step chain.
func TestAnOrdinaryTurnIsUnchangedByThePipeline(t *testing.T) {
	t.Parallel()
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{
		Label: "drafter", TrustTier: types.TrustPrivate,
		Client: NewMockProvider(MockResponse{Content: "the only reply"}),
	})
	a, err := NewAgent(AgentConfig{
		Provider:     NewMockProvider(MockResponse{Content: "unused"}),
		Providers:    reg,
		PrimaryLabel: "drafter",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := NewTurnBudget(BudgetCaps{})
	resp, err := a.RunToolCallLoop(context.Background(), ProcessMessageRequest{
		Message: "hello",
		Claims:  &types.Claims{UserID: "alice"},
		Budget:  budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply != "the only reply" {
		t.Errorf("reply = %q", resp.Reply)
	}
}

// capturingIngester records what memory was offered.
//
// Ingest is fire-and-forget in its own goroutine, so this signals
// rather than being read directly — polling a slice would be a race
// and sleeping would be a slower race.
type capturingIngester struct{ got chan EpisodicTurn }

func newCapturingIngester() *capturingIngester {
	return &capturingIngester{got: make(chan EpisodicTurn, 1)}
}

func (c *capturingIngester) IngestTurn(_ context.Context, turn EpisodicTurn) error {
	select {
	case c.got <- turn:
	default:
	}
	return nil
}

// What the user RECEIVED is what should be remembered.
//
// Ingesting step 0's draft would seed episodic memory — and through it
// dream consolidation — with a reply nobody ever saw, so the assistant
// would later recall having said something it did not say.
func TestMemoryRemembersTheFinalAnswerNotTheDraft(t *testing.T) {
	t.Parallel()
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{
		Label: "drafter", TrustTier: types.TrustPrivate,
		Client: NewMockProvider(MockResponse{Content: "the draft"}),
	})
	reg.Register(ProviderEntry{
		Label: "reviewer", TrustTier: types.TrustPrivate,
		Client: NewMockProvider(MockResponse{Content: "the reviewed reply"}),
	})
	ingester := newCapturingIngester()
	a, err := NewAgent(AgentConfig{
		Provider:         NewMockProvider(MockResponse{Content: "unused"}),
		Providers:        reg,
		PrimaryLabel:     "drafter",
		EpisodicIngester: ingester,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithRoute(context.Background(), &Route{
		StartLabel: "drafter", ChainLabel: "review",
		Steps: []ResolveStep{
			{Provider: config.ProviderConfig{Label: "drafter"}, Role: "primary"},
			{Provider: config.ProviderConfig{Label: "reviewer"}, Role: "reviewer"},
		},
	})
	budget, _ := NewTurnBudget(BudgetCaps{})
	if _, err := a.RunToolCallLoop(ctx, ProcessMessageRequest{
		Message: "what is a raft snapshot?",
		Claims:  &types.Claims{UserID: "alice"},
		Budget:  budget,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case turn := <-ingester.got:
		if turn.AssistReply != "the reviewed reply" {
			t.Errorf("remembered %q; memory holds a reply the user never saw", turn.AssistReply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was ingested")
	}
}
