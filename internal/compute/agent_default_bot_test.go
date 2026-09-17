package compute

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// A turn that names no bot runs as the default team's coordinator.
//
// Bots were a console-only feature by accident: five code paths build
// a turn and only the console ever set BotID, so Telegram, Slack, REST
// and the inbound webhook all ran the pre-bot assistant. You could
// build a team in the browser, message Telegram, and reach somebody
// who had never heard of them.
func TestATurnWithNoBotRunsAsTheCoordinator(t *testing.T) {
	t.Parallel()

	coordinator := &BotProfile{
		ID:            "coordinator",
		DisplayName:   "Coordinator",
		IsCoordinator: true,
		Tools:         []string{"notify"},
	}
	a := &Agent{cfg: AgentConfig{
		Logger:     discardLogger(),
		DefaultBot: func(context.Context) (*BotProfile, error) { return coordinator, nil },
	}}

	req := &ProcessMessageRequest{Message: "hello from telegram"}
	if err := a.fillDefaults(context.Background(), req); err != nil {
		t.Fatalf("fillDefaults: %v", err)
	}
	if req.BotID != "coordinator" {
		t.Errorf("BotID = %q, want the coordinator", req.BotID)
	}
	if req.Bot == nil {
		t.Fatal("no profile resolved, so the tool filter and soul lookup both miss")
	}
}

// An explicit bot always wins. The console names one per room, and a
// default that overrode it would make every room the coordinator.
func TestAnExplicitBotIsNotOverriddenByTheDefault(t *testing.T) {
	t.Parallel()

	a := &Agent{cfg: AgentConfig{
		Logger: discardLogger(),
		DefaultBot: func(context.Context) (*BotProfile, error) {
			return &BotProfile{ID: "coordinator"}, nil
		},
	}}
	req := &ProcessMessageRequest{Message: "hi", BotID: "devops"}
	if err := a.fillDefaults(context.Background(), req); err != nil {
		t.Fatalf("fillDefaults: %v", err)
	}
	if req.BotID != "devops" {
		t.Errorf("BotID = %q, want the bot the caller asked for", req.BotID)
	}
}

// A registry that cannot be reached must not silence the channel.
//
// The failure being avoided is an unanswered Telegram message;
// refusing the turn because the coordinator lookup failed is a worse
// version of the same thing.
func TestAFailedCoordinatorLookupStillAnswers(t *testing.T) {
	t.Parallel()

	a := &Agent{cfg: AgentConfig{
		Logger: discardLogger(),
		DefaultBot: func(context.Context) (*BotProfile, error) {
			return nil, errors.New("raft: not leader")
		},
	}}
	req := &ProcessMessageRequest{Message: "hello"}
	if err := a.fillDefaults(context.Background(), req); err != nil {
		t.Fatalf("a failed lookup failed the turn: %v", err)
	}
	if req.Bot != nil || req.BotID != "" {
		t.Error("a failed lookup left a bot set")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
