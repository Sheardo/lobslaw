package compute

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// recordingLoop captures the request the runner built, which is where
// every invariant worth asserting ends up: the tool list the model
// will be shown, the budget it draws on, the claims it runs as.
type recordingLoop struct {
	mu   sync.Mutex
	reqs []ProcessMessageRequest
	err  error
}

func (l *recordingLoop) RunToolCallLoop(_ context.Context, req ProcessMessageRequest) (*ProcessMessageResponse, error) {
	l.mu.Lock()
	l.reqs = append(l.reqs, req)
	l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	return &ProcessMessageResponse{Reply: "ok"}, nil
}

func (l *recordingLoop) last() ProcessMessageRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reqs[len(l.reqs)-1]
}

type mapResolver map[string]*BotProfile

func (m mapResolver) ResolveBot(_ context.Context, id string) (*BotProfile, error) {
	p, ok := m[id]
	if !ok {
		return nil, errors.New("no such bot")
	}
	return p, nil
}

func testRunner(t *testing.T, loop TurnLoop, bots BotResolver, caps BudgetCaps) *TurnRunner {
	t.Helper()
	r, err := NewTurnRunner(loop, bots, caps, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewTurnRunner: %v", err)
	}
	return r
}

func toolNames(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

var nodeTools = []Tool{
	{Name: "web_search"},
	{Name: "shell_command"},
	{Name: "memory_write"},
	{Name: "ask_bot"},
}

// The registry filter is applied by the agent, not by the runner,
// because the agent is the choke point EVERY turn passes through —
// channels included. Asserting it anywhere else would prove it for
// headless turns only, which is the half that matters least: the GUI
// chats to a bot through a channel.
//
// So these go through a real Agent and read the tool list off the
// provider request, which is the last place it exists before the model
// sees it.
func advertisedTools(t *testing.T, bots BotResolver, req TurnRequest) []string {
	t.Helper()
	mock := NewMockProvider(MockResponse{Content: "done"})
	agent, err := NewAgent(AgentConfig{Provider: mock})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	r := testRunner(t, agent, bots, BudgetCaps{})
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := mock.Calls()
	if len(calls) == 0 {
		t.Fatal("the provider was never called")
	}
	return toolNames(calls[0].Tools)
}

// Isolation has to be STRUCTURAL: the marketing bot is not shown
// shell_command, so there is no refusal to argue with and no policy
// decision to get wrong. Asserted on the tool list the turn was built
// with, not on a policy verdict.
func TestBotIsNeverShownAToolOutsideItsAllowlist(t *testing.T) {
	t.Parallel()
	got := advertisedTools(t, mapResolver{
		"marketing": {ID: "marketing", Tools: []string{"web_search", "memory_write"}},
	}, TurnRequest{
		BotID: "marketing", Prompt: "draft a launch post", Origin: "test", OriginID: "1",
		Tools: nodeTools,
	})

	if len(got) != 2 {
		t.Fatalf("advertised tools = %v, want exactly the two allowed", got)
	}
	for _, banned := range []string{"shell_command", "ask_bot"} {
		if slices.Contains(got, banned) {
			t.Errorf("marketing was shown %q", banned)
		}
	}
}

// An empty allowlist means the node's full set, not the empty set. A
// bot created without anyone stating its tools should be as capable as
// the assistant was before it existed; silently muting one presents as
// a broken bot rather than as a decision somebody made.
func TestEmptyAllowlistMeansEverythingNotNothing(t *testing.T) {
	t.Parallel()
	got := advertisedTools(t, mapResolver{"generalist": {ID: "generalist"}}, TurnRequest{
		BotID: "generalist", Prompt: "hello", Origin: "test", OriginID: "1", Tools: nodeTools,
	})
	if len(got) != len(nodeTools) {
		t.Errorf("advertised %v, want the node's full set", got)
	}
}

// The delegation guard. A child reached through ask_bot has ask_bot
// removed from the registry it is built with, so a second hop is
// unexpressible rather than merely disallowed — no depth counter to
// keep enforcing, and a fork bomb is not a thing that can be typed.
func TestWithoutRemovesAToolEvenFromAnUnrestrictedBot(t *testing.T) {
	t.Parallel()
	unrestricted := &BotProfile{ID: "engineering"}
	got := advertisedTools(t, nil, TurnRequest{
		Profile: unrestricted.Without("ask_bot"),
		Prompt:  "answer this", Origin: "ask", OriginID: "1", Tools: nodeTools,
	})

	if slices.Contains(got, "ask_bot") {
		t.Fatal("a delegated child was still shown ask_bot; one hop is not enforced")
	}
	if len(got) != len(nodeTools)-1 {
		t.Errorf("child lost more than the one tool it should have: %v", got)
	}
}

// Subtracting the only tool a bot had must not fall back through the
// empty-means-everything rule and silently re-grant it. That is why
// the denial is a separate list subtracted last, rather than a
// narrowing of the allowlist.
func TestWithoutEmptyingAnAllowlistDoesNotReGrantEverything(t *testing.T) {
	t.Parallel()
	narrow := &BotProfile{ID: "narrow", Tools: []string{"ask_bot"}}
	got := advertisedTools(t, nil, TurnRequest{
		Profile: narrow.Without("ask_bot"),
		Prompt:  "x", Origin: "ask", OriginID: "1", Tools: nodeTools,
	})
	if len(got) != 0 {
		t.Errorf("emptied allowlist re-granted %v", got)
	}
}

// A bot's turns run as its own principal, so memory ownership, policy
// subjects and audit all attribute to the bot rather than to whoever
// happened to boot the node.
func TestBotTurnRunsAsItsOwnPrincipal(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, mapResolver{"devops": {ID: "devops"}}, BudgetCaps{})

	if _, err := r.Run(context.Background(), TurnRequest{
		BotID: "devops", Prompt: "check the cluster", Origin: "task", OriginID: "1",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	claims := loop.last().Claims
	if claims.UserID != "bot:devops" {
		t.Errorf("claims.UserID = %q, want %q", claims.UserID, "bot:devops")
	}
	if claims.Scope != "bot:devops" {
		t.Errorf("claims.Scope = %q, want %q", claims.Scope, "bot:devops")
	}
}

// Explicit claims win, so a routine somebody scheduled still attributes
// to them for audit even though a bot is doing the work.
func TestExplicitClaimsOverrideTheBotPrincipal(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, mapResolver{"devops": {ID: "devops"}}, BudgetCaps{})

	if _, err := r.Run(context.Background(), TurnRequest{
		BotID: "devops", Prompt: "x", Origin: "task", OriginID: "1",
		Claims: &types.Claims{UserID: "user:alice", Scope: "scheduler"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := loop.last().Claims.UserID; got != "user:alice" {
		t.Errorf("claims.UserID = %q, want the explicit %q", got, "user:alice")
	}
}

// A bot may tighten the operator's caps and must not escape them. The
// operator's number is the one chosen deliberately; a bot record the
// coordinator wrote is not the place to overrule it.
func TestBotCapsTightenButDoNotEscapeTheNodeCaps(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, mapResolver{
		"thrifty":   {ID: "thrifty", Caps: BudgetCaps{MaxToolCalls: 3}},
		"ambitious": {ID: "ambitious", Caps: BudgetCaps{MaxToolCalls: 500}},
	}, BudgetCaps{MaxToolCalls: 10})

	spend := func(botID string) int {
		if _, err := r.Run(context.Background(), TurnRequest{
			BotID: botID, Prompt: "x", Origin: "test", OriginID: "1",
		}); err != nil {
			t.Fatalf("Run %s: %v", botID, err)
		}
		budget := loop.last().Budget
		allowed := 0
		for range 1000 {
			if !budget.RecordToolCall().Within {
				break
			}
			allowed++
		}
		return allowed
	}

	if got := spend("thrifty"); got != 3 {
		t.Errorf("thrifty bot spent %d, want its own tighter cap of 3", got)
	}
	if got := spend("ambitious"); got != 10 {
		t.Errorf("ambitious bot spent %d, want the node's cap of 10 — a bot must not raise it", got)
	}
}

// Delegation's bound: a child's budget draws on the parent's, so a
// tree of turns is limited by the one number the root authorised.
func TestReservationBoundsADelegatedTurn(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, nil, BudgetCaps{MaxToolCalls: 100})
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: 2})

	if _, err := r.Run(context.Background(), TurnRequest{
		Prompt: "x", Origin: "ask", OriginID: "1", Reservation: reservation,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	budget := loop.last().Budget
	allowed := 0
	for range 50 {
		if budget.RecordToolCall().Within {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("delegated turn spent %d against a reservation of 2", allowed)
	}
}

func TestRunnerRefusesAnEmptyPrompt(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, nil, BudgetCaps{})

	_, err := r.Run(context.Background(), TurnRequest{Prompt: "  ", Origin: "task", OriginID: "t1"})
	if err == nil {
		t.Fatal("ran a turn with no prompt; that is a provider call asking the model to answer its own system prompt")
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Errorf("error does not name the record that caused it: %v", err)
	}
	if len(loop.reqs) != 0 {
		t.Error("the loop was entered anyway")
	}
}

// No bot means the node's default assistant — exactly what every turn
// did before bots existed, so the upgrade changes nothing for a
// deployment that never creates one.
func TestNoBotRunsAsTheNodeDefault(t *testing.T) {
	t.Parallel()
	loop := &recordingLoop{}
	r := testRunner(t, loop, nil, BudgetCaps{})

	if _, err := r.Run(context.Background(), TurnRequest{
		Prompt: "hello", Origin: "task", OriginID: "1", Tools: nodeTools,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := loop.last()
	if req.Bot != nil || req.BotID != "" {
		t.Errorf("a turn with no bot carried one: %v / %q", req.Bot, req.BotID)
	}
	if len(req.Tools) != len(nodeTools) {
		t.Errorf("tools were filtered with no bot to filter by: %v", toolNames(req.Tools))
	}
}

// The brief is appended to the operator's soul body, not substituted
// for it. Replacing would let creating a bot silently opt out of house
// style and safety guidance written for all of them.
func TestBotBriefIsAppendedToTheSoulBody(t *testing.T) {
	t.Parallel()
	const body = "Operator baseline: be concise."
	got := appendBotBrief(body, &BotProfile{
		ID: "engineering", DisplayName: "Engineering", Instructions: "You are the engineer for XYZ.",
	})
	if !strings.Contains(got, body) {
		t.Error("the operator's soul body was dropped")
	}
	if !strings.Contains(got, "You are the engineer for XYZ.") {
		t.Error("the bot's brief is missing")
	}
	if !strings.Contains(got, "You are Engineering.") {
		t.Error("the bot is not named to itself")
	}
	if strings.Index(got, body) > strings.Index(got, "engineer for XYZ") {
		t.Error("the brief precedes the baseline; the baseline governs it")
	}
}

func TestNoBriefLeavesTheSoulBodyByteIdentical(t *testing.T) {
	t.Parallel()
	const body = "Operator baseline."
	if got := appendBotBrief(body, nil); got != body {
		t.Errorf("no bot changed the body: %q", got)
	}
	if got := appendBotBrief(body, &BotProfile{ID: "quiet"}); got != body {
		t.Errorf("a bot with no brief changed the body: %q", got)
	}
}

func TestMayMessageIsAnAllowlist(t *testing.T) {
	t.Parallel()
	p := &BotProfile{ID: "marketing", MayMessage: []string{"engineering"}}
	if !p.MayMessageBot("engineering") {
		t.Error("a declared edge was refused")
	}
	if p.MayMessageBot("devops") {
		t.Error("an undeclared edge was allowed; the graph is an allowlist")
	}
	if (&BotProfile{ID: "lonely"}).MayMessageBot("anyone") {
		t.Error("a bot with no declared edges could reach one")
	}
}
