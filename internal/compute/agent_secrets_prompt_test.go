package compute

import (
	"context"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The prompt is where "lobslaw knows it has a vault" actually lives, and
// promptgen's own test only proves the renderer. This covers the link
// that one cannot: AgentConfig → RuntimeInfo → the system prompt the
// model is handed.
//
// It matters because the failure is silent and confident. Asked where
// to keep an API key on a node with a vault wired up, a model told
// nothing about it suggests an environment variable — or invites the
// user to paste the key into the chat, which puts it in the transcript,
// the session store, and the next turn's replay.
func TestAgentPromptCarriesConfiguredVaults(t *testing.T) {
	t.Parallel()

	var captured string
	provider := NewMockProviderFunc(func(req ChatRequest, _ int) (MockResponse, error) {
		// The system prompt is the first message, per ChatRequest's
		// own contract.
		if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
			captured = req.Messages[0].Content
		}
		return MockResponse{Content: "ok", FinishReason: "stop"}, nil
	})

	a, err := NewAgent(AgentConfig{
		Provider:             provider,
		Soul:                 func() *types.SoulConfig { return &types.SoulConfig{} },
		SecretProviderLabels: []string{"bw", "vault"},
	})
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := NewTurnBudget(BudgetCaps{})
	if _, err := a.RunToolCallLoop(context.Background(), ProcessMessageRequest{
		Message: "where should I put my Stripe key?",
		Budget:  budget,
	}); err != nil {
		t.Fatalf("turn: %v", err)
	}

	if !strings.Contains(captured, "secret_vaults: bw, vault") {
		t.Errorf("the configured vaults never reached the prompt:\n%s", captured)
	}
	if !strings.Contains(captured, "offer it without being asked") {
		t.Errorf("the instruction to volunteer the vault is missing:\n%s", captured)
	}
	if !strings.Contains(captured, "Never ask for the secret itself in chat") {
		t.Errorf("the instruction that keeps a secret out of the transcript is missing:\n%s", captured)
	}
}

// A node with no vault describes none, rather than advertising a
// capability it does not have.
func TestAgentPromptOmitsVaultsWhenNoneConfigured(t *testing.T) {
	t.Parallel()

	var captured string
	provider := NewMockProviderFunc(func(req ChatRequest, _ int) (MockResponse, error) {
		// The system prompt is the first message, per ChatRequest's
		// own contract.
		if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
			captured = req.Messages[0].Content
		}
		return MockResponse{Content: "ok", FinishReason: "stop"}, nil
	})

	a, err := NewAgent(AgentConfig{
		Provider: provider,
		Soul:     func() *types.SoulConfig { return &types.SoulConfig{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := NewTurnBudget(BudgetCaps{})
	if _, err := a.RunToolCallLoop(context.Background(), ProcessMessageRequest{
		Message: "hi", Budget: budget,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captured, "secret_vaults") {
		t.Errorf("no vault is configured, so none should be described:\n%s", captured)
	}
}
