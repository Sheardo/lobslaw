package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/turn"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

type fakeBotResolver map[string]*compute.BotProfile

func (f fakeBotResolver) ResolveBot(_ context.Context, id string) (*compute.BotProfile, error) {
	p, ok := f[id]
	if !ok {
		return nil, errors.New("no such bot")
	}
	return p, nil
}

// spyLoop stands in for the agent loop so the delegated turn's shape —
// the tools it was given, the budget it drew on — can be read back.
type spyLoop struct {
	mu    sync.Mutex
	reqs  []compute.ProcessMessageRequest
	reply string
	err   error
}

func (l *spyLoop) RunToolCallLoop(_ context.Context, req compute.ProcessMessageRequest) (*compute.ProcessMessageResponse, error) {
	l.mu.Lock()
	l.reqs = append(l.reqs, req)
	l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	return &compute.ProcessMessageResponse{Reply: l.reply}, nil
}

func (l *spyLoop) last() compute.ProcessMessageRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reqs[len(l.reqs)-1]
}

func askBotHandler(t *testing.T, loop *spyLoop, bots fakeBotResolver) compute.BuiltinFunc {
	t.Helper()
	runner, err := compute.NewTurnRunner(loop, bots, compute.BudgetCaps{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewTurnRunner: %v", err)
	}
	return newAskBotHandler(runner, bots, nil)
}

func asBot(ctx context.Context, botID string) context.Context {
	return turn.WithIdentity(ctx, turn.Identity{BotID: botID, TurnID: "turn-1"})
}

// The graph is an allowlist. A bot that was never granted an edge
// cannot reach the target, and the refusal says what is missing rather
// than pretending the bot does not exist — the caller is one of the
// assistant's own agents and an oracle-style evasion would only make
// it retry.
func TestAskBotRefusesAnUndeclaredEdge(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{reply: "sure"}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing"},
		"engineering": {ID: "engineering"},
	}
	_, _, err := askBotHandler(t, loop, bots)(asBot(context.Background(), "marketing"),
		map[string]string{"bot_id": "engineering", "question": "is this claim true?"})

	if err == nil {
		t.Fatal("an undeclared edge was allowed")
	}
	if !strings.Contains(err.Error(), "may_message") {
		t.Errorf("error does not name the missing grant: %v", err)
	}
	if len(loop.reqs) != 0 {
		t.Error("the delegated turn ran anyway")
	}
}

func TestAskBotFollowsADeclaredEdge(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{reply: "yes, that is accurate"}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing", MayMessage: []string{"engineering"}},
		"engineering": {ID: "engineering"},
	}
	out, _, err := askBotHandler(t, loop, bots)(asBot(context.Background(), "marketing"),
		map[string]string{"bot_id": "engineering", "question": "is this claim true?"})
	if err != nil {
		t.Fatalf("ask_bot: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["answer"] != "yes, that is accurate" {
		t.Errorf("answer = %v", got["answer"])
	}
	// The child is told who is asking. A question with no attribution
	// reads as an instruction from nowhere.
	if !strings.Contains(loop.last().Message, "marketing") {
		t.Errorf("the child was not told who asked: %q", loop.last().Message)
	}
}

// One hop, structurally. The child is built from a registry with
// ask_bot absent, so a second hop is unexpressible rather than
// disallowed — no counter anybody has to keep enforcing, and a fork
// bomb is not a thing that can be typed.
func TestAskBotChildCannotAskAnyoneElse(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{reply: "ok"}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing", MayMessage: []string{"engineering"}},
		"engineering": {ID: "engineering", MayMessage: []string{"devops"}},
		"devops":      {ID: "devops"},
	}
	ctx := asBot(context.Background(), "marketing")
	// The advertised list the child would otherwise see.
	nodeTools := []compute.Tool{{Name: "web_search"}, {Name: "ask_bot"}, {Name: "inbox_post"}}

	if _, _, err := askBotHandler(t, loop, bots)(ctx,
		map[string]string{"bot_id": "engineering", "question": "q"}); err != nil {
		t.Fatalf("ask_bot: %v", err)
	}

	child := loop.last()
	if child.Bot == nil {
		t.Fatal("the child ran with no profile; nothing was narrowed")
	}
	for _, tool := range child.Bot.FilterTools(nodeTools) {
		if tool.Name == "ask_bot" {
			t.Fatal("the child was still shown ask_bot; one hop is not enforced")
		}
	}
	// And it keeps everything else — the guard removes one tool, not
	// the child's ability to do its job.
	if len(child.Bot.FilterTools(nodeTools)) != len(nodeTools)-1 {
		t.Errorf("the child lost more than ask_bot: %v", child.Bot.FilterTools(nodeTools))
	}
}

// The delegated turn draws on the CALLER's budget, so a tree of asks
// is bounded by the one number the top-level turn was authorised for
// however the tree is shaped.
func TestAskBotDrawsOnTheCallersBudget(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{reply: "ok"}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing", MayMessage: []string{"engineering"}},
		"engineering": {ID: "engineering"},
	}
	reservation, _ := compute.NewTurnBudget(compute.BudgetCaps{MaxToolCalls: 2})
	ctx := compute.WithBudget(asBot(context.Background(), "marketing"), reservation)

	if _, _, err := askBotHandler(t, loop, bots)(ctx,
		map[string]string{"bot_id": "engineering", "question": "q"}); err != nil {
		t.Fatalf("ask_bot: %v", err)
	}

	child := loop.last().Budget
	allowed := 0
	for range 20 {
		if child.RecordToolCall().Within {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("the delegated turn spent %d against the caller's reservation of 2", allowed)
	}
}

// A child that cannot answer fails the tool call and says who refused.
// It cannot ask for a confirmation: the person is in a conversation
// with the CALLER, and the prompt registry carries RaisedFor precisely
// so the wrong party cannot answer one.
func TestAskBotFailsClosedAndNamesTheBot(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{err: errors.New("shell:run requires confirmation")}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing", MayMessage: []string{"engineering"}},
		"engineering": {ID: "engineering"},
	}
	_, _, err := askBotHandler(t, loop, bots)(asBot(context.Background(), "marketing"),
		map[string]string{"bot_id": "engineering", "question": "q"})
	if err == nil {
		t.Fatal("a child that could not answer returned success")
	}
	for _, want := range []string{"engineering", "confirmation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestAskBotRefusesItself(t *testing.T) {
	t.Parallel()
	loop := &spyLoop{reply: "ok"}
	bots := fakeBotResolver{"marketing": {ID: "marketing", MayMessage: []string{"marketing"}}}
	_, _, err := askBotHandler(t, loop, bots)(asBot(context.Background(), "marketing"),
		map[string]string{"bot_id": "marketing", "question": "q"})
	if err == nil {
		t.Error("a bot asked itself")
	}
}

// The inbox belongs to a bot. A turn with no bot — a person talking to
// the node default — has no queue to read, and saying so is better
// than inventing one.
func TestInboxToolsRefuseATurnWithNoBot(t *testing.T) {
	t.Parallel()
	_, err := callerBot(context.Background())
	if err == nil {
		t.Fatal("callerBot invented an identity")
	}
	if !strings.Contains(err.Error(), "belongs to a bot") {
		t.Errorf("error does not explain: %v", err)
	}
}

// The sender is stamped from the turn, never from args — a bot that
// could name its own sender could post work that appears to come from
// somebody else.
func TestInboxPostStampsTheSenderFromTheTurn(t *testing.T) {
	t.Parallel()
	rec := &recordingInbox{}
	bots := fakeBotResolver{
		"marketing":   {ID: "marketing", MayMessage: []string{"engineering"}},
		"engineering": {ID: "engineering"},
	}
	handler := newInboxPostHandler(rec, bots)
	if _, _, err := handler(asBot(context.Background(), "marketing"), map[string]string{
		"bot_id": "engineering",
		"body":   "please deploy",
		"sender": "bot:chief",
	}); err != nil {
		t.Fatalf("inbox_post: %v", err)
	}
	if got := rec.posted[0].GetSender(); got != "bot:marketing" {
		t.Errorf("sender = %q, want it taken from the turn", got)
	}
}

type recordingInbox struct {
	posted []*lobslawv1.BotInboxItem
}

func (r *recordingInbox) Post(_ context.Context, item *lobslawv1.BotInboxItem) (*lobslawv1.BotInboxItem, error) {
	item.Id = "item-1"
	item.Status = lobslawv1.InboxStatus_INBOX_STATUS_PENDING
	r.posted = append(r.posted, item)
	return item, nil
}

func (r *recordingInbox) Get(context.Context, string, string) (*lobslawv1.BotInboxItem, error) {
	return nil, errors.New("not used")
}

func (r *recordingInbox) List(context.Context, string, memory.InboxFilter) ([]*lobslawv1.BotInboxItem, error) {
	return nil, nil
}

func (r *recordingInbox) Resolve(context.Context, string, string, memory.InboxOutcome) (*lobslawv1.BotInboxItem, error) {
	return nil, nil
}
