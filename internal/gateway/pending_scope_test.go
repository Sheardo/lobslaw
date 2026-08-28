package gateway

import (
	"testing"
)

// pendingScope remembers what a confirmation is about so an "approve
// for this chat" tap can record a matching grant. Every path that
// resolves a prompt has to consume it: the plain approve and deny
// verbs never needed it, and the grant helpers used to return early
// when no approvals store was wired. Both left the entry behind, and
// nothing else ever removes one — a slow leak keyed by prompts nobody
// will tap again, on a process designed to run for months.

func TestTelegramTakePendingScopeIsIdempotent(t *testing.T) {
	t.Parallel()

	h := &TelegramHandler{
		pendingScope: map[string]scopedOperation{
			"p1": {action: "tool:exec", resource: "write_file"},
		},
	}

	op, ok := h.takePendingScope("p1")
	if !ok || op.resource != "write_file" {
		t.Fatalf("first take = (%+v,%v), want the recorded operation", op, ok)
	}
	if _, ok := h.takePendingScope("p1"); ok {
		t.Fatal("a second take replayed an operation that was already consumed")
	}
	if len(h.pendingScope) != 0 {
		t.Fatalf("map still holds %d entries", len(h.pendingScope))
	}
}

// The failing case that motivated the fix: no approvals store wired,
// so the grant cannot be recorded — and the entry used to survive.
func TestTelegramGrantWithoutStoreStillConsumesTheScope(t *testing.T) {
	t.Parallel()

	h := &TelegramHandler{
		cfg: TelegramConfig{}, // nil Approvals and ApprovalRules
		log: discardLogger(),
		pendingScope: map[string]scopedOperation{
			"p1": {action: "tool:exec", resource: "write_file", subject: "user:alice"},
			"p2": {action: "tool:exec", resource: "write_file", subject: "user:alice"},
		},
	}
	q := &tgCallbackQuery{Message: &tgMessage{Chat: tgChat{ID: 1, Type: "private"}}}

	if h.grantForSession(t.Context(), "p1", q) {
		t.Error("a session grant was reported with no approvals store")
	}
	if h.grantAlways(t.Context(), "p2", q) {
		t.Error("a permanent grant was reported with no rules store")
	}
	if len(h.pendingScope) != 0 {
		t.Fatalf("failed grants left %d entries behind", len(h.pendingScope))
	}
}

func TestSlackTakePendingScopeIsIdempotent(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{
		pendingScope: map[string]scopedOperation{
			"p1": {action: "tool:exec", resource: "write_file"},
		},
	}
	if _, ok := h.takePendingScope("p1"); !ok {
		t.Fatal("first take found nothing")
	}
	if _, ok := h.takePendingScope("p1"); ok {
		t.Fatal("a second take replayed a consumed operation")
	}
	if len(h.pendingScope) != 0 {
		t.Fatalf("map still holds %d entries", len(h.pendingScope))
	}
}
