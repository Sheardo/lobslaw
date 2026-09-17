package gateway

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// An inbound Telegram message reaches the coordinator, not the
// pre-bot assistant.
//
// This was the half of the channel bridge nothing exercised. Outbound
// was proved against a live Telegram and Slack; inbound was wired and
// never once driven, and the failure it guards against is silent —
// the turn still answers, just as somebody who has never heard of
// your team.
//
// Driven through the real webhook handler rather than by calling the
// agent directly, because the thing under test is precisely that a
// path which never set BotID now gets one filled in for it.
func TestAnInboundTelegramMessageReachesTheCoordinator(t *testing.T) {
	t.Parallel()

	var resolved atomic.Int32
	coordinator := &compute.BotProfile{
		ID:            "coordinator",
		DisplayName:   "Coordinator",
		IsCoordinator: true,
		Instructions:  "You run the team.",
	}

	provider := compute.NewMockProvider(compute.MockResponse{Content: "on it"})
	agent, err := compute.NewAgent(compute.AgentConfig{
		Provider: provider,
		DefaultBot: func(context.Context) (*compute.BotProfile, error) {
			resolved.Add(1)
			return coordinator, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	h := newTGHarness(t, agent, TelegramConfig{UnknownUserScope: "owner"})
	rec := postUpdate(t, h.handler, tgTestSecret, `{
		"update_id": 1,
		"message": {
			"message_id": 1,
			"from": {"id": 5053517285, "username": "james"},
			"chat": {"id": 5053517285, "type": "private"},
			"text": "who is on your team?",
			"date": 1700000000
		}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook returned %d: %s", rec.Code, rec.Body.String())
	}

	if resolved.Load() == 0 {
		t.Error("the turn never asked who the default bot is; Telegram is still " +
			"running the pre-bot assistant")
	}
	sent := h.sentMessages()
	if len(sent) != 1 || sent[0].Text != "on it" {
		t.Errorf("reply did not come back: %+v", sent)
	}
}

// A node with no registry answers exactly as it did before bots.
//
// The resolver returning nil has to mean "no bot", not "no reply":
// falling over here would take out the channel on every deployment
// that has not created a team.
func TestInboundStillWorksWithNoTeam(t *testing.T) {
	t.Parallel()

	provider := compute.NewMockProvider(compute.MockResponse{Content: "still here"})
	agent, err := compute.NewAgent(compute.AgentConfig{
		Provider:   provider,
		DefaultBot: func(context.Context) (*compute.BotProfile, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	h := newTGHarness(t, agent, TelegramConfig{UnknownUserScope: "owner"})
	rec := postUpdate(t, h.handler, tgTestSecret, `{
		"update_id": 2,
		"message": {
			"message_id": 2,
			"from": {"id": 999, "username": "someone"},
			"chat": {"id": 999, "type": "private"},
			"text": "hello",
			"date": 1700000000
		}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook returned %d", rec.Code)
	}
	if sent := h.sentMessages(); len(sent) != 1 || sent[0].Text != "still here" {
		t.Errorf("a node with no team stopped answering: %+v", sent)
	}
}
