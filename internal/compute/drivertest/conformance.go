// Package drivertest is the shared conformance suite every provider
// driver must pass.
//
// It exists because the vendors move. Over one afternoon of reading
// vendor documentation the provider design changed three times, and
// each change was a vendor doing something the previous draft assumed
// nobody did. A suite that pins the properties the layer above depends
// on is what turns "add a driver" from an adventure into a known
// quantity, and what catches a vendor changing shape underneath.
//
// The suite asserts CONTRACT, not behaviour: that a driver classifies
// its failures, reports its usage in a unit, and honours cancellation.
// What it actually says to the model is the driver's own business.
package drivertest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// Subject is the driver under test plus what the suite needs to
// exercise it.
type Subject struct {
	Name string

	// Chat is the driver, or nil if it does not serve chat.
	Chat compute.ChatDriver

	// FailingChat is the same driver pointed at an endpoint that
	// returns the given status, so failure classification can be
	// checked without hitting a real provider. Optional: a driver that
	// cannot be pointed at a fake endpoint skips those cases, and the
	// suite says so rather than passing quietly.
	FailingChat func(status int, body string) compute.ChatDriver

	// Live marks a subject wired to a real endpoint. Live subjects skip
	// the failure-injection cases (a real provider will not return 402
	// on demand) and run the round-trip cases for real.
	Live bool

	// Model names the model the suite asks for, and exists because
	// against a fake it does not matter and against a real provider it
	// is the difference between a round trip and a 404. Empty keeps the
	// placeholder, which is what every fake-backed subject wants.
	Model string
}

// model is what the suite puts on the wire.
func (s Subject) model() string {
	if s.Model != "" {
		return s.Model
	}
	return "test-model"
}

// callCtx bounds a live call. A fake answers immediately, but a real
// provider that hangs would otherwise burn the whole `go test` timeout
// and report as the suite hanging rather than the provider.
func (s Subject) callCtx(t *testing.T) context.Context {
	t.Helper()
	if !s.Live {
		return context.Background()
	}
	ctx, cancel := context.WithTimeout(context.Background(), LiveTimeout)
	t.Cleanup(cancel)
	return ctx
}

// Run executes every applicable case. Call it from a driver's own
// test file:
//
//	drivertest.Run(t, drivertest.Subject{Name: "openai", Chat: d, ...})
func Run(t *testing.T, s Subject) {
	t.Helper()
	if s.Chat == nil {
		t.Fatalf("%s: subject serves no modality — nothing to conform to", s.Name)
	}
	t.Run(s.Name+"/chat", func(t *testing.T) {
		t.Run("round trip", func(t *testing.T) { chatRoundTrip(t, s) })
		t.Run("reports usage in a unit", func(t *testing.T) { chatUsage(t, s) })
		t.Run("honours cancellation", func(t *testing.T) { chatCancel(t, s) })
		t.Run("classifies failures", func(t *testing.T) { chatFailures(t, s) })
	})
}

func chatRoundTrip(t *testing.T, s Subject) {
	resp, err := s.Chat.Chat(s.callCtx(t), compute.ChatRequest{
		Model:    s.model(),
		Messages: []compute.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response with nil error")
	}
	// Content or tool calls — a well-behaved provider returns at least
	// one, and a driver that returns neither has swallowed something.
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		t.Error("response carried neither content nor tool calls")
	}
	if resp.FinishReason == "" {
		t.Error("no finish reason; the agent loop branches on it")
	}
}

// The layer above prices calls from usage. A driver that returns a
// zero token count on a token-billed call makes the turn look free.
func chatUsage(t *testing.T, s Subject) {
	resp, err := s.Chat.Chat(s.callCtx(t), compute.ChatRequest{
		Model:    s.model(),
		Messages: []compute.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.Usage.TotalTokens == 0 && resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		t.Error("no usage reported; every call downstream of this prices at zero")
	}
}

// A turn can be cancelled by the responsiveness hard timeout while the
// model is still generating. A driver that ignores ctx holds the
// session lease and the conversation with it.
func chatCancel(t *testing.T, s Subject) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Chat.Chat(ctx, compute.ChatRequest{
		Model:    s.model(),
		Messages: []compute.Message{{Role: "user", Content: "ping"}},
	})
	if err == nil {
		t.Fatal("a cancelled context produced a successful call")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want something wrapping context.Canceled — callers branch on it", err)
	}
}

// The property the three-class failover taxonomy rests on. An
// unclassified error defaults to permanent, so a driver that does not
// classify silently disables failover for its own transient failures.
func chatFailures(t *testing.T, s Subject) {
	if s.Live {
		t.Skip("live subject: a real provider will not return 402 on demand")
	}
	if s.FailingChat == nil {
		t.Skip("subject provides no failure injection — classification unverified")
	}

	cases := []struct {
		name   string
		status int
		body   string
		want   compute.FailureClass
		why    string
	}{
		{"server error", 503, `{"error":"upstream"}`, compute.FailureTransient,
			"a 5xx is worth trying on the backup"},
		{"rate limit", 429, `{"error":{"type":"rate_limit_error"}}`, compute.FailureTransient,
			"rate limits pass"},
		{"quota exhausted", 429, `{"error":{"type":"insufficient_quota"}}`, compute.FailureQuotaExhausted,
			"a spent plan is not a rate limit: it must not be retried until reset, and it must be loud"},
		{"payment required", 402, `{"error":"credit balance is too low"}`, compute.FailureQuotaExhausted,
			"402 is the unambiguous quota signal"},
		{"bad request", 400, `{"error":"unknown model"}`, compute.FailurePermanent,
			"a 400 fails identically on the backup; falling through multiplies the error"},
		// This used to expect permanent, on the reasoning that a bad key
		// is an operator problem and failing over spends the backup's
		// quota to paper over it. The concern was right; the conclusion
		// was not. Permanent means one rotated key takes the assistant
		// down while two working providers sit idle, and the user's
		// experience of a config fault should not be "it stopped
		// replying".
		//
		// What made permanent defensible was that nothing else made the
		// fault visible. logProviderFailure now reports a credential
		// rejection at ERROR, saying plainly that the chain is covering
		// for it — so the operator finds out AND the assistant keeps
		// working, instead of trading one for the other.
		{"unauthorized", 401, `{"error":"bad key"}`, compute.FailureCredential,
			"the next provider has its own key; this one is logged loudly rather than failing the turn"},
		{"forbidden", 403, `{"error":"key lacks permission"}`, compute.FailureCredential,
			"same as 401 — the credential is the problem, and it is this provider's credential"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := s.FailingChat(tc.status, tc.body)
			_, err := d.Chat(context.Background(), compute.ChatRequest{
				Model:    s.model(),
				Messages: []compute.Message{{Role: "user", Content: "ping"}},
			})
			if err == nil {
				t.Fatalf("HTTP %d produced no error", tc.status)
			}
			if got := compute.ClassifyFailure(err); got != tc.want {
				t.Errorf("HTTP %d classified %s, want %s — %s\n  error: %v",
					tc.status, got, tc.want, tc.why, err)
			}
		})
	}
}

// LiveSubjectFromEnv reports whether live-endpoint tests should run,
// and is the only thing that reads credentials.
//
// Credentials are never committed and never passed as test flags: a
// flag lands in shell history and CI logs. The suite reads the
// provider's own conventional environment variable and skips when it
// is absent, so `go test ./...` is always safe to run.
func LiveSubjectFromEnv(t *testing.T, envVar string) (string, bool) {
	t.Helper()
	key := strings.TrimSpace(getenv(envVar))
	if key == "" {
		t.Skipf("%s not set; skipping live driver test", envVar)
		return "", false
	}
	if !liveEnabled() {
		t.Skipf("LOBSLAW_LIVE_DRIVER_TESTS not set; skipping live driver test (%s is present)", envVar)
		return "", false
	}
	return key, true
}

// LiveTimeout bounds a live call so a hung provider fails the test
// rather than the suite.
const LiveTimeout = 60 * time.Second

func getenv(k string) string { return os.Getenv(k) }

// liveEnabled requires a second, explicit opt-in beyond the presence
// of a key. A developer with OPENAI_API_KEY exported for unrelated
// reasons should not start spending money because they ran the test
// suite.
func liveEnabled() bool { return os.Getenv("LOBSLAW_LIVE_DRIVER_TESTS") != "" }
