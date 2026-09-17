package compute

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Provenance must survive the hop that destroys it.
//
// "Prepare a report and send it to James" reaches the coordinator as
// Sam, becomes a queue item for research, and by the time research
// sends anything the turn runs as bot:research. Without carrying the
// requester the chain ends there and James receives a message with no
// idea who asked — which is the reading that matters, because anybody
// able to talk to a bot could otherwise make it send messages that
// look system-originated.
func TestTheRequesterSurvivesDelegation(t *testing.T) {
	t.Parallel()

	a := &Agent{cfg: AgentConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	// The delegated turn: running AS a bot, for work Sam asked for.
	id := a.TurnIdentityFor(ProcessMessageRequest{
		BotID:       "research",
		Claims:      &types.Claims{UserID: "bot:research"},
		RequestedBy: "user:sam",
	})
	if id.RequestedBy != "user:sam" {
		t.Errorf("RequestedBy = %q, want the person at the start of the chain", id.RequestedBy)
	}
	// The bot is still the one doing the work.
	if id.Principal.String() != "bot:research" {
		t.Errorf("Principal = %q, want the bot running the turn", id.Principal.String())
	}
}

// Work nobody asked for stays unattributed. A routine at 3am has no
// requester, and inventing one is worse than having none.
func TestUnrequestedWorkNamesNobody(t *testing.T) {
	t.Parallel()

	a := &Agent{cfg: AgentConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	id := a.TurnIdentityFor(ProcessMessageRequest{
		BotID:  "devops",
		Claims: &types.Claims{UserID: "bot:devops"},
	})
	if id.RequestedBy != "" {
		t.Errorf("RequestedBy = %q, want empty for work nobody asked for", id.RequestedBy)
	}
}
