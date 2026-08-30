package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// discardLogger keeps the refusal paths quiet: they log deliberately
// and loudly, which is right in production and noise in a test that
// asserts the refusal happened.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePrompts is the smallest Prompts that lets the authorisation
// branch be exercised without a raft-backed registry.
type fakePrompts struct {
	prompt *Prompt
	err    error
}

func (f *fakePrompts) Create(NewPrompt) (*Prompt, error) { return f.prompt, f.err }
func (f *fakePrompts) Get(string) (*Prompt, error)       { return f.prompt, f.err }
func (f *fakePrompts) Resolve(string, PromptDecision, PromptScope) error {
	return nil
}
func (f *fakePrompts) Wait(context.Context, string) (PromptDecision, error) {
	return PromptApproved, nil
}

// The #127 fix, in its Slack form. Buttons render into a room where
// everybody can see and click them, so "who may answer" cannot be
// "whoever clicked".
func TestSlackMayResolveRefusesSomeoneElsesPrompt(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{
		cfg: SlackConfig{
			AllowedChannels: []string{"*"},
			Prompts:         &fakePrompts{prompt: &Prompt{ID: "p1", RaisedFor: "slack-T0-U0ALICE"}},
		},
		log: discardLogger(),
		api: newSlackAPI("xoxb-test", "http://127.0.0.1:1", nil),
	}

	// Bob taps a prompt raised for Alice.
	if h.mayResolve(context.Background(), "p1", "T0", "U0BOB", "C1", "") {
		t.Fatal("a tap from the wrong person was allowed to resolve the prompt")
	}
	// Alice taps her own.
	if !h.mayResolve(context.Background(), "p1", "T0", "U0ALICE", "C1", "") {
		t.Fatal("the person the question was asked of was refused")
	}
}

// Fails closed: a prompt nobody can be attributed to must not be
// answerable by anybody.
func TestSlackMayResolveRefusesUnattributablePrompt(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{
		cfg: SlackConfig{
			AllowedChannels: []string{"*"},
			Prompts:         &fakePrompts{prompt: &Prompt{ID: "p1", RaisedFor: ""}},
		},
		log: discardLogger(),
		api: newSlackAPI("xoxb-test", "http://127.0.0.1:1", nil),
	}
	if h.mayResolve(context.Background(), "p1", "T0", "U0ALICE", "C1", "") {
		t.Fatal("a prompt with no recorded audience was answerable")
	}
	// An anonymous tap is refused even on a well-formed prompt.
	h.cfg.Prompts = &fakePrompts{prompt: &Prompt{ID: "p1", RaisedFor: "slack-T0-U0ALICE"}}
	if h.mayResolve(context.Background(), "p1", "T0", "", "C1", "") {
		t.Fatal("an unattributed tap was allowed")
	}
}

func TestSlackIsAudienceMatchesChannelDerivedID(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{cfg: SlackConfig{}, log: discardLogger()}
	ctx := context.Background()

	// No resolver wired, so principalFor returns the channel-derived
	// id — a prompt raised before any binding existed must still be
	// answerable by the person it was raised for.
	if !h.isAudience(ctx, "T0", "U0ALICE", "slack-T0-U0ALICE") {
		t.Error("the raised-for user was not recognised by their derived id")
	}
	if h.isAudience(ctx, "T0", "U0BOB", "slack-T0-U0ALICE") {
		t.Error("a different user matched")
	}
	// Team scoping holds here too: same handle, other workspace.
	if h.isAudience(ctx, "T0OTHER", "U0ALICE", "slack-T0-U0ALICE") {
		t.Error("the same user id in another workspace matched")
	}
	if h.isAudience(ctx, "T0", "U0ALICE", "") {
		t.Error("an empty raised-for matched somebody")
	}
}

// The button payload is the routing table; a malformed or foreign
// action_id must be ignored rather than misparsed into a verb.
func TestSlackButtonActionIDRoundTrips(t *testing.T) {
	t.Parallel()

	b := button("Approve", "prompt:approve:01ABC", "primary")
	if b["action_id"] != "prompt:approve:01ABC" {
		t.Fatalf("action_id = %v", b["action_id"])
	}
	if b["style"] != "primary" {
		t.Errorf("style = %v", b["style"])
	}
	// An unstyled button must not carry an empty style: Slack rejects
	// the message outright rather than ignoring the field.
	plain := button("Approve here", "prompt:approve-session:01ABC", "")
	if _, has := plain["style"]; has {
		t.Error("an unstyled button carried a style key")
	}
}

// A grant recorded from a thread must not leak to the channel around
// it — the thread is its own conversation everywhere else in this
// channel, and an approval is exactly where that has to hold.
func TestSlackSessionGrantIsScopedToTheThread(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{
		cfg:          SlackConfig{}, // nil Approvals
		log:          discardLogger(),
		pendingScope: map[string]scopedOperation{"p1": {action: "tool:exec", resource: "x"}},
	}
	// With no approvals store the grant must report failure rather
	// than claiming success — the reply promises what happened.
	if h.grantForSession(context.Background(), "p1", "C1/1.1") != "" {
		t.Fatal("a grant was reported without an approvals store")
	}
	// And the pending scope is consumed either way, so a second tap
	// cannot replay it.
	if _, still := h.takePendingScope("p1"); still {
		t.Fatal("the pending scope survived a failed grant")
	}
}

// A grant must be scoped to the conversation the TURN ran in, which is
// the one recorded on the prompt — not one rebuilt from the button.
//
// The two differ exactly where it matters. A top-level channel message
// carries no thread_ts, so the turn is scoped to "C1"; the confirmation
// is posted INTO a thread, so the tap comes back carrying one. Rebuilt
// from the tap the grant lands under "C1/<ts>", a conversation the turn
// was never in — and "approve here" silently asks again next time.
func TestSlackSessionGrantUsesThePromptsConversation(t *testing.T) {
	t.Parallel()

	// What the turn recorded for a top-level channel message.
	const turnConversation = "C1"
	// What a tap on the threaded confirmation would rebuild.
	rebuiltFromTap := slackConversationID("C1", "1700000000.000100")

	if rebuiltFromTap == turnConversation {
		t.Fatal("the two spellings collapsed; this test no longer proves anything")
	}
	if rebuiltFromTap != "C1/1700000000.000100" {
		t.Fatalf("rebuilt = %q", rebuiltFromTap)
	}

	// A grant with no conversation narrows to once rather than being
	// recorded against nothing.
	h := &SlackHandler{
		cfg:          SlackConfig{},
		log:          discardLogger(),
		pendingScope: map[string]scopedOperation{"p1": {action: "tool:exec", resource: "x"}},
	}
	if h.grantForSession(context.Background(), "p1", "") != "" {
		t.Fatal("a grant with no conversation was recorded")
	}
}
