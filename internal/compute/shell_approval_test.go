package compute

import (
	"context"
	"errors"
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

// "Always allow" on shell used to mean every shell command forever.
// Nobody should tap that, so nobody did, and the operator went back to
// editing config — which is what this gate exists to stop.
//
// Underneath it was worse: a substring denylist inside the builtin
// refused sudo, ssh, curl and ten others outright, with no approval
// path at all. The answer to "let me run this one ssh" was to edit a
// Go file.
//
// Exercised against a REAL policy engine rather than a stub, for the
// reason write_approval_test.go gives: the gate is a policy question,
// so a stub would assert the plumbing and none of the behaviour that
// matters — which rule wins, and whether a grant satisfies it.

func shellGatedExecutor(t *testing.T, rules ...*lobslawv1.PolicyRule) (*Executor, *SessionApprovals) {
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

	// The allow every node carries from wire_seeds.go. Its presence is
	// the point: the gate must not be satisfied by it.
	rules = append(rules, &lobslawv1.PolicyRule{
		Id: "lobslaw-builtin-shell_command", Subject: "*",
		Action: "tool:exec", Resource: "shell_command",
		Effect: "allow", Priority: 1,
	})
	for _, r := range rules {
		raw, err := proto.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(memory.BucketPolicyRules, r.Id, raw); err != nil {
			t.Fatal(err)
		}
	}

	eng := policy.NewEngine(store, slog.New(slog.DiscardHandler))
	eng.SetDefaults([]types.PolicyRule{ShellApprovalDefault()})

	approvals := NewSessionApprovals()
	e := NewExecutor(NewRegistry(), eng, nil, ExecutorConfig{}, slog.New(slog.DiscardHandler))
	e.SetSessionApprovals(approvals)
	e.RequireCommandApproval("shell_command", ShellGrantResource, ShellCommandSummary)
	return e, approvals
}

func shellParams(cmd string) map[string]string {
	return map[string]string{"command": cmd}
}

func checkShell(t *testing.T, e *Executor, ctx context.Context, cmd string) error {
	t.Helper()
	return e.checkGate(ctx, &types.Claims{UserID: "alice"}, "shell_command", shellParams(cmd))
}

// The default: a command the operator has not spoken about is asked
// about. This is what replaced the denylist, and it is strictly safer
// — the denylist asked about nothing and refused thirteen things.
func TestAnUnapprovedCommandIsAsked(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t)

	if err := checkShell(t, e, context.Background(), "git status --short"); !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("err = %v, want ErrRequireConfirm", err)
	}
}

// The trap write_approval.go warns about, made concrete: every node
// seeds allow tool:exec/shell_command at priority 1, so a gate asking
// under tool:exec would be satisfied before it was asked.
func TestTheShellGateUsesItsOwnAction(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t)

	err := checkShell(t, e, context.Background(), "git status")
	if !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("the tool:exec allow satisfied the gate: %v", err)
	}
	var cr *ConfirmationRequest
	if !errors.As(err, &cr) {
		t.Fatal("the confirmation did not carry its operation")
	}
	if cr.Action != ShellAction {
		t.Errorf("Action = %q, want %q", cr.Action, ShellAction)
	}
}

// The whole point of the design. An approval names one command, so the
// next command is still asked about — this is what "ssh host" covering
// "ssh host rm -rf ~" would have broken.
func TestAGrantCoversOnlyTheCommandItNamed(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t, &lobslawv1.PolicyRule{
		Id: "approval:prompt-1", Subject: "user:alice",
		Action: ShellAction, Resource: "git status --short",
		Effect: "allow", Priority: 1,
	})

	if err := checkShell(t, e, context.Background(), "git status --short"); err != nil {
		t.Errorf("an approved command was asked about again: %v", err)
	}
	for _, other := range []string{
		"git push --force",
		"git status",
		"rm -rf /home/james",
	} {
		if err := checkShell(t, e, context.Background(), other); !errors.Is(err, ErrRequireConfirm) {
			t.Errorf("the grant leaked to %q: %v", other, err)
		}
	}
}

// Deliberate generalisation lives in an operator rule, where it is
// written down and revocable. This is the answer to "stop asking me
// about git" — edit the config ONCE, rather than constantly.
func TestAnOperatorGlobCoversAClass(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t, &lobslawv1.PolicyRule{
		Id: "james-git-is-fine", Subject: "*",
		Action: ShellAction, Resource: "git *",
		Effect: "allow", Priority: 20,
	})

	for _, cmd := range []string{"git status", "git push --force", "git log --oneline"} {
		if err := checkShell(t, e, context.Background(), cmd); err != nil {
			t.Errorf("the operator glob did not cover %q: %v", cmd, err)
		}
	}
	if err := checkShell(t, e, context.Background(), "rm -rf /tmp/x"); !errors.Is(err, ErrRequireConfirm) {
		t.Errorf("a git glob covered something that is not git: %v", err)
	}
}

func TestASessionGrantSatisfiesTheShellGate(t *testing.T) {
	t.Parallel()
	e, approvals := shellGatedExecutor(t)
	ctx := WithTurnIdentity(context.Background(), TurnIdentity{
		Principal: identity.Principal("user:alice"), Channel: "telegram", ChannelID: "42",
	})

	err := checkShell(t, e, ctx, "git status --short")
	if !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("the first call was not asked about: %v", err)
	}
	var cr *ConfirmationRequest
	if !errors.As(err, &cr) {
		t.Fatal("the confirmation did not carry its operation")
	}
	// Exactly what the channel does with a "for this chat" tap.
	approvals.Grant(ctx, cr.Action, cr.Resource)

	if err := checkShell(t, e, ctx, "git status --short"); err != nil {
		t.Errorf("a granted command was asked about again: %v", err)
	}
	if err := checkShell(t, e, ctx, "git push --force"); !errors.Is(err, ErrRequireConfirm) {
		t.Errorf("the session grant leaked to another command: %v", err)
	}
}

// A compound command has no stable form to remember, so it is asked
// about every time and nothing may be minted from it. The empty
// resource is how both channels already suppress the session and
// always buttons, so this needs no channel change.
func TestACompoundCommandOffersNoGrant(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t)

	err := checkShell(t, e, context.Background(), "git status && rm -rf ~")
	if !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("err = %v, want ErrRequireConfirm", err)
	}
	var cr *ConfirmationRequest
	if !errors.As(err, &cr) {
		t.Fatal("the confirmation did not carry its operation")
	}
	if cr.Resource != "" {
		t.Errorf("Resource = %q, want empty so no scope button is offered", cr.Resource)
	}
	if !strings.Contains(cr.Summary, "git status && rm -rf ~") {
		t.Errorf("Summary = %q; the user cannot see what would run", cr.Summary)
	}
}

// An operator who allows the sentinel has explicitly said "stop asking
// me about compound commands". Nothing else reaches it.
func TestAllowingTheSentinelCoversCompoundCommands(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t, &lobslawv1.PolicyRule{
		Id: "operator-accepts-compound", Subject: "*",
		Action: ShellAction, Resource: ShellUnclassified,
		Effect: "allow", Priority: 20,
	})

	if err := checkShell(t, e, context.Background(), "git status && make"); err != nil {
		t.Errorf("the sentinel allow did not cover a compound command: %v", err)
	}
	if err := checkShell(t, e, context.Background(), "git status"); !errors.Is(err, ErrRequireConfirm) {
		t.Errorf("the sentinel allow leaked to an ordinary command: %v", err)
	}
}

// A deny is a deny. Attaching the command to it would put it in front
// of somebody who is not being asked to decide about it.
func TestADeniedCommandCarriesNoContent(t *testing.T) {
	t.Parallel()
	e, _ := shellGatedExecutor(t, &lobslawv1.PolicyRule{
		Id: "operator-forbids", Subject: "*",
		Action: ShellAction, Resource: "*",
		Effect: "deny", Priority: 50,
	})

	err := checkShell(t, e, context.Background(), "git status --short")
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("err = %v, want ErrPolicyDenied", err)
	}
	if strings.Contains(err.Error(), "git status --short") {
		t.Errorf("a denial carried the command: %q", err)
	}
}

// A default that could outrank an operator's rule would not be a
// default.
func TestTheShellDefaultRuleIsTheLowestPriority(t *testing.T) {
	t.Parallel()
	if got := ShellApprovalDefault().Priority; got != -1<<30 {
		t.Errorf("Priority = %d, want %d", got, -1<<30)
	}
	if got := ShellApprovalDefault().Effect; got != types.EffectRequireConfirmation {
		t.Errorf("Effect = %v, want require_confirmation", got)
	}
}

// The gate is per-tool. A deployment that never registered
// shell_command carries no extra check rather than one that always
// passes.
func TestAnUngatedToolIsNotShellChecked(t *testing.T) {
	t.Parallel()
	// No policy engine at all: if the gate consulted one for a tool it
	// was not registered against, this fails with ErrNoPolicyEngine
	// rather than passing.
	e := NewExecutor(NewRegistry(), nil, nil, ExecutorConfig{}, slog.New(slog.DiscardHandler))

	if err := e.checkGate(context.Background(), &types.Claims{UserID: "alice"},
		"read_file", shellParams("git status")); err != nil {
		t.Fatalf("an ungated tool was checked: %v", err)
	}
}
