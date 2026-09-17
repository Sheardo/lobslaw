package gateway

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// An inbound Slack message reaches the coordinator too.
//
// Same gap as Telegram, same silence: the turn answers either way, and
// the only symptom of getting it wrong is an assistant that has never
// heard of the team you built. Both channels are asserted because the
// resolution happens in one shared place precisely so it cannot be
// wired for one and missed for the other — which is a claim worth
// testing rather than trusting.
func TestAnInboundSlackMessageReachesTheCoordinator(t *testing.T) {
	t.Parallel()

	var resolved atomic.Int32
	agent, err := compute.NewAgent(compute.AgentConfig{
		Provider: compute.NewMockProvider(compute.MockResponse{Content: "on it"}),
		DefaultBot: func(context.Context) (*compute.BotProfile, error) {
			resolved.Add(1)
			return &compute.BotProfile{
				ID: "coordinator", DisplayName: "Coordinator", IsCoordinator: true,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	hh := newSlackHarness(t, agent,
		SlackConfig{AllowedChannels: []string{"*"}, UnknownUserScope: "owner"})
	hh.h.handleEvent(context.Background(), dmEvent("who is on your team?"))

	if resolved.Load() == 0 {
		t.Error("the turn never asked who the default bot is; Slack is still " +
			"running the pre-bot assistant")
	}
	got := hh.posted()
	if len(got) == 0 || got[len(got)-1] != "on it" {
		t.Errorf("reply did not come back: %v", got)
	}
}
