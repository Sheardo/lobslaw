package compute

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/identity"
	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/policy"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Approving a confirmation has to RUN THE THING.
//
// When a turn pauses, its last two messages are the assistant's tool
// call and a tool-role result saying "tool invocation requires
// confirmation". Resuming replayed exactly that into the loop, so the
// first thing the model saw on resume was its own call having been
// refused — and a model that has just been told it was refused does not
// try again. It explains the refusal instead.
//
// The user therefore tapped Approve and got a paragraph about needing
// approval, and every retry raised a fresh prompt. The approval was
// recorded correctly, the policy gate would have passed, and none of
// that mattered because the call was never re-issued.
//
// Approving names an operation. Resuming re-runs THAT operation and
// replaces the refusal with its real result, rather than asking the
// model to decide again from a transcript that says no.

func resumeAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(dir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rule := &lobslawv1.PolicyRule{
		Id: "lobslaw-builtin-shell_command", Subject: "*",
		Action: "tool:exec", Resource: "shell_command",
		Effect: "allow", Priority: 1,
	}
	raw, err := proto.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(memory.BucketPolicyRules, rule.Id, raw); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	eng := policy.NewEngine(store, log)
	eng.SetDefaults([]types.PolicyRule{ShellApprovalDefault()})

	reg := NewRegistry()
	if err := reg.Register(ShellToolDef()); err != nil {
		t.Fatal(err)
	}
	builtins := NewBuiltins()
	if err := RegisterShellBuiltin(builtins); err != nil {
		t.Fatal(err)
	}

	e := NewExecutor(reg, eng, nil, ExecutorConfig{}, log)
	e.SetBuiltins(builtins)
	e.SetSessionApprovals(NewSessionApprovals())
	e.RequireCommandApproval("shell_command", ShellGrantResource, ShellCommandSummary)

	// One tool call, then a text reply once the model has a result.
	mock := NewMockProvider(
		MockResponse{ToolCalls: []ToolCall{{
			ID: "call-1", Name: "shell_command",
			Arguments: `{"command":"echo hello-from-the-shell"}`,
		}}},
		MockResponse{Content: "done"},
		MockResponse{Content: "done"},
	)
	a, err := NewAgent(AgentConfig{Provider: mock, Executor: e, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func resumeReq(t *testing.T) ProcessMessageRequest {
	t.Helper()
	return ProcessMessageRequest{
		Message:   "run echo for me",
		Claims:    &types.Claims{UserID: "alice"},
		TurnID:    "turn-1",
		Channel:   "telegram",
		ChannelID: "42",
		Budget:    mkBudget(t, BudgetCaps{}),
	}
}

func TestApprovingAConfirmationActuallyRunsTheCommand(t *testing.T) {
	t.Parallel()
	a := resumeAgent(t)
	ctx := WithTurnIdentity(context.Background(), TurnIdentity{
		Principal: identity.Principal("user:alice"), Channel: "telegram", ChannelID: "42",
	})
	req := resumeReq(t)

	resp, err := a.RunToolCallLoop(ctx, req)
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if !resp.NeedsConfirmation {
		t.Fatalf("the shell call was not staged for confirmation; reply = %q", resp.Reply)
	}

	// Exactly what the channel does: relax the budget, carry the
	// answer, replay the messages the turn stopped on.
	req.Budget.Relax()
	approved := WithTurnApproval(ctx, resp.ConfirmationAction, resp.ConfirmationResource)
	resumed, err := a.ResumeFromConfirmation(approved, req, resp.Messages)
	if err != nil {
		t.Fatalf("ResumeFromConfirmation: %v", err)
	}

	// The command must actually have run. Its output is the proof —
	// a resume that only re-asked the model would produce prose about
	// needing approval and no tool output anywhere.
	var ran bool
	for _, m := range resumed.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "hello-from-the-shell") {
			ran = true
		}
	}
	if !ran {
		t.Errorf("the approved command never ran; messages = %s", messageTrace(resumed.Messages))
	}
	if resumed.NeedsConfirmation {
		t.Errorf("the resumed turn asked for confirmation again: %q", resumed.ConfirmationReason)
	}
}

// The refusal must not survive into the resumed transcript either. A
// model that can still see "requires confirmation" in its history will
// keep explaining the refusal even when the call has since succeeded.
func TestTheRefusalIsReplacedByTheRealResult(t *testing.T) {
	t.Parallel()
	a := resumeAgent(t)
	ctx := WithTurnIdentity(context.Background(), TurnIdentity{
		Principal: identity.Principal("user:alice"), Channel: "telegram", ChannelID: "42",
	})
	req := resumeReq(t)

	resp, err := a.RunToolCallLoop(ctx, req)
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if !resp.NeedsConfirmation {
		t.Fatal("no confirmation was raised")
	}

	req.Budget.Relax()
	approved := WithTurnApproval(ctx, resp.ConfirmationAction, resp.ConfirmationResource)
	resumed, err := a.ResumeFromConfirmation(approved, req, resp.Messages)
	if err != nil {
		t.Fatalf("ResumeFromConfirmation: %v", err)
	}

	for _, m := range resumed.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, ErrRequireConfirm.Error()) {
			t.Errorf("the refusal is still in the transcript the model reads:\n%s",
				messageTrace(resumed.Messages))
			break
		}
	}
}

func messageTrace(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		b.WriteString("  [")
		b.WriteString(m.Role)
		b.WriteString("] ")
		content := m.Content
		if len(content) > 160 {
			content = content[:160] + "…"
		}
		b.WriteString(strings.ReplaceAll(content, "\n", " "))
		if len(m.ToolCalls) > 0 {
			b.WriteString(" (tool_calls: ")
			for _, tc := range m.ToolCalls {
				b.WriteString(tc.Name)
			}
			b.WriteString(")")
		}
		if i < len(msgs)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
