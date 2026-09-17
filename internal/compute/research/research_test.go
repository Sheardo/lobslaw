package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// This package shipped with no tests, which is the reason two defects
// in it went unnoticed: the run's tool budget did not add up, and
// research_start's description told the model a total bound existed.
// The tests below COUNT tool calls rather than read caps back out of
// a struct — a fan-out's bound is a property of what it spends.

// greedyAgent is a worker that keeps calling tools until its budget
// refuses. Real workers stop when they have an answer; a worker that
// never stops is what the reservation exists to survive.
//
// It stands in for the agent LOOP, not for the turn runner. The runner
// under test is the real one, because the budget a worker gets is
// something it derives — a fake runner would make every assertion here
// a statement about the fake.
type greedyAgent struct {
	mu        sync.Mutex
	callsMade int
	budgets   []*compute.TurnBudget
	// attempts is how many tool calls each worker tries before giving
	// up, high enough to outrun any per-worker share.
	attempts int
}

func (g *greedyAgent) RunToolCallLoop(_ context.Context, req compute.ProcessMessageRequest) (*compute.ProcessMessageResponse, error) {
	g.mu.Lock()
	g.budgets = append(g.budgets, req.Budget)
	g.mu.Unlock()

	for range g.attempts {
		if !req.Budget.RecordToolCall().Within {
			break
		}
		g.mu.Lock()
		g.callsMade++
		g.mu.Unlock()
	}
	return &compute.ProcessMessageResponse{Reply: "finding for: " + req.Message}, nil
}

func (g *greedyAgent) total() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.callsMade
}

func (g *greedyAgent) runner(t *testing.T) *compute.TurnRunner {
	t.Helper()
	r, err := compute.NewTurnRunner(g, nil, compute.BudgetCaps{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewTurnRunner: %v", err)
	}
	return r
}

// scriptedProvider answers the planner with a JSON array sized to the
// requested depth and the synthesiser with a fixed report.
type scriptedProvider struct {
	mu        sync.Mutex
	subqCount int
	calls     int
}

func (p *scriptedProvider) Chat(_ context.Context, req compute.ChatRequest) (*compute.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()

	// The planner runs first and wants a JSON array; every later call
	// is the synthesiser.
	if n > 1 {
		return &compute.ChatResponse{Content: "the report"}, nil
	}
	subqs := make([]string, p.subqCount)
	for i := range subqs {
		subqs[i] = fmt.Sprintf("sub-question %d", i)
	}
	out, err := json.Marshal(subqs)
	if err != nil {
		return nil, err
	}
	return &compute.ChatResponse{Content: string(out)}, nil
}

type nopMemory struct{}

func (nopMemory) WriteEpisodic(context.Context, string, []string) (string, error) {
	return "mem-1", nil
}

func quietCoordinator(t *testing.T, agent *greedyAgent, provider compute.LLMProvider, maxToolCalls int) *Coordinator {
	t.Helper()
	return NewCoordinator(Config{
		Agent:        agent.runner(t),
		LLMProvider:  provider,
		Memory:       nopMemory{},
		WorkerTools:  []compute.Tool{{Name: "web_search"}},
		MaxToolCalls: maxToolCalls,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// The headline acceptance criterion from R33a: a depth-10 fan-out
// cannot exceed the aggregate reservation. Asserted by counting, not
// by inspecting configuration.
func TestDeepRunCannotExceedTheReservation(t *testing.T) {
	t.Parallel()
	const reservation = 24
	agent := &greedyAgent{attempts: 50}
	provider := &scriptedProvider{subqCount: 10}
	c := quietCoordinator(t, agent, provider, reservation)

	if _, err := c.Run(context.Background(), Request{
		TaskID:   "task-1",
		Question: "everything about everything",
		Depth:    10,
		Claims:   &types.Claims{UserID: "user:test"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := agent.total(); got != reservation {
		t.Errorf("a depth-10 run spent %d tool calls against a reservation of %d", got, reservation)
	}
}

// Depth is a shape parameter, not a spend multiplier. Before the
// reservation, doubling depth doubled the authorised spend while the
// tool description claimed otherwise.
func TestDepthSpreadsTheBudgetRatherThanMultiplyingIt(t *testing.T) {
	t.Parallel()
	const reservation = 24
	spendAtDepth := func(depth int) int {
		agent := &greedyAgent{attempts: 50}
		c := quietCoordinator(t, agent, &scriptedProvider{subqCount: depth}, reservation)
		if _, err := c.Run(context.Background(), Request{
			TaskID:   "task",
			Question: "q",
			Depth:    depth,
			Claims:   &types.Claims{UserID: "user:test"},
		}); err != nil {
			t.Fatalf("Run at depth %d: %v", depth, err)
		}
		return agent.total()
	}

	shallow, deep := spendAtDepth(3), spendAtDepth(9)
	if shallow != deep {
		t.Errorf("depth 3 spent %d and depth 9 spent %d; depth must not change the total", shallow, deep)
	}
	if deep > reservation {
		t.Errorf("depth 9 spent %d, over the reservation of %d", deep, reservation)
	}
}

// Every worker must draw on the same reservation. A worker holding a
// budget with no parent is a worker outside the bound — the exact
// shape of the original defect.
//
// Demonstrated by starvation: with a reservation smaller than what the
// workers collectively demand, an early worker consumes it and the
// later ones get nothing. Independent per-worker budgets could not
// produce that; they would each spend their own share happily.
func TestEveryWorkerDrawsOnTheSharedReservation(t *testing.T) {
	t.Parallel()
	const (
		reservation = 4
		workers     = 4
	)
	agent := &greedyAgent{attempts: 100}
	c := quietCoordinator(t, agent, &scriptedProvider{subqCount: workers}, reservation)

	if _, err := c.Run(context.Background(), Request{
		TaskID:   "task",
		Question: "q",
		Depth:    workers,
		Claims:   &types.Claims{UserID: "user:test"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(agent.budgets) != workers {
		t.Fatalf("got %d worker budgets, want %d", len(agent.budgets), workers)
	}
	for i, b := range agent.budgets {
		if b == nil {
			t.Fatalf("worker %d ran with no budget at all", i)
		}
	}
	if got := agent.total(); got != reservation {
		t.Errorf("workers were allowed %d calls against a reservation of %d "+
			"(independent budgets would allow far more)", got, reservation)
	}
	if last := agent.budgets[workers-1]; !last.Check().Exceeded {
		t.Error("the last worker still reported within after earlier workers " +
			"consumed the reservation; the workers are not sharing one")
	}
}

// A worker's own share is a fairness bound: it stops one bad worker
// consuming the reservation before its siblings run at all.
func TestOneGreedyWorkerLeavesBudgetForItsSiblings(t *testing.T) {
	t.Parallel()
	agent := &greedyAgent{attempts: 1000}
	c := quietCoordinator(t, agent, &scriptedProvider{subqCount: 3}, 24)

	if _, err := c.Run(context.Background(), Request{
		TaskID:   "task",
		Question: "q",
		Depth:    3,
		Claims:   &types.Claims{UserID: "user:test"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(agent.budgets) != 3 {
		t.Fatalf("got %d workers, want 3", len(agent.budgets))
	}
	for i, b := range agent.budgets {
		if got := b.State().ToolCalls; got <= 1 {
			t.Errorf("worker %d only attempted %d calls — an earlier worker starved it", i, got)
		}
	}
}

func TestWorkerShareFloorsRatherThanVanishing(t *testing.T) {
	t.Parallel()
	// The floor means shares can sum past the reservation at high
	// depth. That is intentional — the reservation enforces the total;
	// a share too small to finish one search helps nobody.
	if got := workerShare(24, 3); got != 8 {
		t.Errorf("workerShare(24, 3) = %d, want 8", got)
	}
	if got := workerShare(24, 10); got != minWorkerToolCalls {
		t.Errorf("workerShare(24, 10) = %d, want the floor %d", got, minWorkerToolCalls)
	}
	if got := workerShare(24, 0); got != 24 {
		t.Errorf("workerShare(24, 0) = %d, want the whole reservation", got)
	}
}

// R33 found this as a documentation-versus-thing seam: the package doc
// named a handler ref — "compute:research" — that nothing registers.
// Nothing reads a doc comment, which is exactly why it drifted, so the
// guard has to be a test that does.
func TestPackageDocNamesTheRegisteredHandlerRef(t *testing.T) {
	t.Parallel()
	const registered = "research:run" // == tools.ResearchHandlerRef

	src, err := os.ReadFile("research.go")
	if err != nil {
		t.Fatalf("read research.go: %v", err)
	}
	doc, _, found := strings.Cut(string(src), "\npackage research")
	if !found {
		t.Fatal("could not isolate the package doc comment")
	}
	if strings.Contains(doc, "compute:research") {
		t.Errorf("package doc still names compute:research; the registered ref is %q", registered)
	}
	if !strings.Contains(doc, registered) {
		t.Errorf("package doc does not name the registered ref %q", registered)
	}
}

func (g *greedyAgent) ResumeFromConfirmation(_ context.Context, _ compute.ProcessMessageRequest, _ []compute.Message) (*compute.ProcessMessageResponse, error) {
	return nil, errors.New("ResumeFromConfirmation: not expected in this test")
}
