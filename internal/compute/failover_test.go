package compute

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// The backup chain had no tests at all, which is how it kept its own
// private idea of what "retryable" means: a substring scan over the
// error TEXT ("429", "500", "timeout") written before the driver waist
// existed. The waist now classifies failures structurally, and the two
// disagree in cases that cost real money.
//
// These tests pin the decision the chain must make for each class:
//
//	permanent       → stop. It fails identically on the backup, so
//	                  walking the chain multiplies one error into N.
//	transient       → advance. That is what a backup is for.
//	quota exhausted → advance. THIS provider is spent; the next one
//	                  has its own budget. Stopping here turns "one
//	                  provider ran out of credit" into "the assistant
//	                  is down", which is the exact outage failover
//	                  exists to prevent.

// scriptedProvider fails with a fixed error until the call budget is
// spent, then succeeds — enough to tell "walked the chain" from
// "stopped at the first failure".
type scriptedProvider struct {
	label string
	err   error
	calls *[]string
}

func (p *scriptedProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	*p.calls = append(*p.calls, p.label)
	if p.err != nil {
		return nil, p.err
	}
	return &ChatResponse{Content: "ok from " + p.label, FinishReason: "stop"}, nil
}

// twoProviderAgent wires primary → backup, where primary fails with
// failWith and backup always succeeds.
func twoProviderAgent(t *testing.T, failWith error) (*Agent, *[]string) {
	t.Helper()
	calls := &[]string{}
	reg := NewProviderRegistry()
	reg.Register(ProviderEntry{
		Label:  "primary",
		Backup: "backup",
		Client: &scriptedProvider{label: "primary", err: failWith, calls: calls},
	})
	reg.Register(ProviderEntry{
		Label:  "backup",
		Client: &scriptedProvider{label: "backup", calls: calls},
	})
	a := &Agent{cfg: AgentConfig{
		Provider:     &scriptedProvider{label: "unused", calls: calls},
		Providers:    reg,
		PrimaryLabel: "primary",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	return a, calls
}

func TestFailoverAdvancesOnEachClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		advance bool
		why     string
	}{
		{
			name:    "transient",
			err:     Transient(errors.New("upstream exploded")),
			advance: true,
			why:     "a transient failure is the whole reason a backup is configured",
		},
		{
			name:    "quota exhausted",
			err:     QuotaExhausted(errors.New("credit balance is too low")),
			advance: true,
			why:     "this provider is spent, the next has its own budget; stopping here is a self-inflicted outage",
		},
		{
			name:    "permanent",
			err:     Permanent(errors.New("unknown model")),
			advance: false,
			why:     "it fails identically on the backup; walking the chain multiplies one error into N",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, calls := twoProviderAgent(t, tc.err)

			resp, err := a.dispatchWithBackup(context.Background(), ChatRequest{
				Messages: []Message{{Role: "user", Content: "ping"}},
			})

			walked := len(*calls) > 1
			if walked != tc.advance {
				t.Errorf("chain walked = %v, want %v — %s\n  calls: %v",
					walked, tc.advance, tc.why, *calls)
			}
			if tc.advance {
				if err != nil {
					t.Errorf("backup should have served the turn, got error: %v", err)
				} else if resp.resp.Content != "ok from backup" {
					t.Errorf("reply came from %q, want the backup", resp.resp.Content)
				}
			} else if err == nil {
				t.Error("a permanent failure produced a successful turn")
			}
		})
	}
}

// A cancelled turn must not spend the backup's quota. The user gave up
// or the hard timeout fired; retrying inside the same dead context
// buys nothing and bills for it.
func TestFailoverStopsOnCancellation(t *testing.T) {
	t.Parallel()
	a, calls := twoProviderAgent(t, Transient(errors.New("upstream exploded")))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := a.dispatchWithBackup(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "ping"}},
	}); err == nil {
		t.Fatal("a cancelled turn produced a successful call")
	}
	if len(*calls) > 1 {
		t.Errorf("walked the chain on a cancelled turn (calls: %v); the backup was billed for nothing", *calls)
	}
}

// Not every provider wraps its errors in a DriverError yet. Those must
// keep the old text-scan behaviour rather than silently becoming
// permanent — an unclassified 503 that stops failing over would be a
// regression introduced by tightening the classifier.
func TestFailoverKeepsHeuristicForUnclassifiedErrors(t *testing.T) {
	t.Parallel()
	a, calls := twoProviderAgent(t, errors.New("provider returned 503 service unavailable"))

	resp, err := a.dispatchWithBackup(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unclassified transient error did not fail over: %v", err)
	}
	if resp.resp.Content != "ok from backup" {
		t.Errorf("reply came from %q, want the backup (calls: %v)", resp.resp.Content, *calls)
	}
}
