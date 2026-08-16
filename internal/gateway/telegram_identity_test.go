package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/identity"
)

// The Telegram edge is the only place that sees the sender's numeric
// id — tgUserIdentity throws it away in favour of the @username, which
// is reassignable. So resolution has to happen here or not at all.

type stubBindings map[string]string

func (s stubBindings) PrincipalFor(_ context.Context, channel, address string) (string, error) {
	return s[channel+":"+address], nil
}

// identityOf drives one message through the handler and returns the
// UserID the turn was run under.
func identityOf(t *testing.T, cfg TelegramConfig, update string) string {
	t.Helper()
	spy := &turnIdentitySpy{}
	agent, err := compute.NewAgent(compute.AgentConfig{
		Provider: compute.NewMockProvider(compute.MockResponse{Content: "ack"}),
		Hooks:    spy,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newTGHarness(t, agent, cfg)
	if rec := postUpdate(t, h.handler, tgTestSecret, update); rec.Code != http.StatusOK {
		t.Fatalf("update rejected: %d", rec.Code)
	}
	return spy.identity(t).UserID
}

const aliceUpdate = `{
	"update_id": 1,
	"message": {
		"message_id": 1,
		"from": {"id": 123456789, "username": "alice"},
		"chat": {"id": 222, "type": "private"},
		"text": "hello"
	}
}`

// A rename must not orphan somebody's history and grants. Bound by
// numeric id, the handle is irrelevant.
func TestTelegramResolvesTheBoundPrincipal(t *testing.T) {
	t.Parallel()
	got := identityOf(t, TelegramConfig{
		UnknownUserScope: "public",
		Identity: identity.NewResolver(nil).
			WithBindings(stubBindings{"telegram:123456789": "alice"}),
	}, aliceUpdate)

	if got != "alice" {
		t.Errorf("UserID = %q, want the canonical principal the operator bound", got)
	}
}

// Somebody who claims a freed handle must inherit nothing.
func TestTelegramDoesNotHandTheHandleToAStranger(t *testing.T) {
	t.Parallel()
	const stranger = `{
		"update_id": 2,
		"message": {
			"message_id": 2,
			"from": {"id": 999999999, "username": "alice"},
			"chat": {"id": 222, "type": "private"},
			"text": "hello"
		}
	}`
	got := identityOf(t, TelegramConfig{
		UnknownUserScope: "public",
		Identity: identity.NewResolver(nil).
			WithBindings(stubBindings{"telegram:123456789": "alice"}),
	}, stranger)

	if got == "alice" {
		t.Fatal("a stranger using Alice's old handle was run as Alice")
	}
	if got != "tg-@alice" {
		t.Errorf("UserID = %q, want the unbound sender to be their own principal", got)
	}
}

// A deployment binding nothing behaves exactly as before, whether or
// not a resolver is wired at all.
func TestTelegramIdentityUnchangedWithoutBindings(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]TelegramConfig{
		"no resolver": {UnknownUserScope: "public"},
		"resolver with nothing bound": {
			UnknownUserScope: "public",
			Identity:         identity.NewResolver(nil).WithBindings(stubBindings{}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := identityOf(t, cfg, aliceUpdate); got != "tg-@alice" {
				t.Errorf("UserID = %q, want tg-@alice", got)
			}
		})
	}
}
