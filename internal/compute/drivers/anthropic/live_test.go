package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivertest"
)

// The driver above is proven against a fake in anthropic_test.go, and
// that fake was written by the same person who wrote the driver. If the
// understanding of the Messages API encoded in the driver is wrong, the
// fake encodes the same misunderstanding and both tests pass. Only a
// real endpoint can tell the difference.
//
// So these tests exist to check the TRANSLATION, not the plumbing:
// whether Anthropic accepts the shape we send. They need two things to
// run — a key in ANTHROPIC_API_KEY and an explicit LOBSLAW_LIVE_DRIVER_TESTS
// opt-in — so `go test ./...` neither fails without credentials nor
// spends money because someone happened to have a key exported.
//
//	LOBSLAW_LIVE_DRIVER_TESTS=1 go test -run Live ./internal/compute/...

// liveModel is the smallest current model, because these tests assert
// on shapes rather than answers and there is no reason to pay for
// intelligence. Overridable for checking the driver against a model
// with different behaviour.
func liveModel() string {
	if m := strings.TrimSpace(os.Getenv("LOBSLAW_ANTHROPIC_TEST_MODEL")); m != "" {
		return m
	}
	return "claude-haiku-4-5"
}

func liveDriver(t *testing.T) *Driver {
	t.Helper()
	key, ok := drivertest.LiveSubjectFromEnv(t, "ANTHROPIC_API_KEY")
	if !ok {
		return nil
	}
	d, err := New(Config{
		Model:      liveModel(),
		Credential: compute.NewHeaderCredential("x-api-key", key),
		// Small: these tests read shapes, not prose, and a driver that
		// let a model ramble would just be slower and dearer.
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The same contract the fake-backed subject satisfies, against the real
// endpoint. Failure injection is skipped — the API will not return 402
// on request — so what this adds over the fake is that the request we
// build is one Anthropic actually accepts.
func TestLiveConformance(t *testing.T) {
	d := liveDriver(t)
	drivertest.Run(t, drivertest.Subject{
		Name:  "anthropic-live",
		Chat:  d,
		Live:  true,
		Model: liveModel(),
	})
}

// The assertion the fake cannot make. Every structural choice in
// toWire is a guess until a real endpoint validates it, and each guess
// fails as a 400 rather than as a wrong answer:
//
//   - system hoisted to a top-level field (a system role in messages[]
//     is rejected outright);
//   - a tool definition carrying input_schema rather than a nested
//     function object;
//   - an assistant message carrying a tool_use block;
//   - a tool result as a USER message with a tool_result block whose
//     tool_use_id matches.
//
// The transcript is synthetic rather than driven by the model choosing
// to call the tool, because the point is to test OUR shape
// deterministically — a test that depends on the model deciding to use
// a tool is a test that fails on a Tuesday for no reason.
func TestLiveAcceptsTheTranslatedTranscript(t *testing.T) {
	d := liveDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), drivertest.LiveTimeout)
	defer cancel()

	resp, err := d.Chat(ctx, compute.ChatRequest{
		Messages: []compute.Message{
			{Role: "system", Content: "You are a weather reporter. Answer in one short sentence."},
			{Role: "user", Content: "What is the weather in Paris?"},
			{Role: "assistant", ToolCalls: []compute.ToolCall{{
				ID:        "toolu_01ABCDEFGHIJKLMNOPQRSTUV",
				Name:      "get_weather",
				Arguments: `{"city":"Paris"}`,
			}}},
			{Role: "tool", ToolCallID: "toolu_01ABCDEFGHIJKLMNOPQRSTUV", Content: "18C, raining"},
		},
		Tools: []compute.Tool{{
			Name:        "get_weather",
			Description: "Get the current weather for a city.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{"city":{"type":"string","description":"City name"}},
				"required":["city"]
			}`),
		}},
	})
	if err != nil {
		// A 400 here means the translation is wrong, and the body says
		// which part — that is the whole value of this test.
		t.Fatalf("Anthropic rejected the translated transcript: %v", err)
	}
	if resp.Content == "" {
		t.Error("no content in the reply after a tool result round trip")
	}
	// The model was told the weather by the tool result. If the tool
	// result had been dropped rather than translated, the reply could
	// not carry it — so this checks the result reached the model, not
	// merely that the request parsed.
	if !strings.Contains(resp.Content, "18") {
		t.Errorf("reply %q does not reflect the tool result; it may have been accepted but ignored", resp.Content)
	}
	if resp.Usage.TotalTokens == 0 {
		t.Error("no usage reported on a live call")
	}
	t.Logf("live reply: %q (finish=%s, prompt=%d completion=%d cached=%d)",
		resp.Content, resp.FinishReason,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.CachedTokens)
}

// The system prompt is the one translation that fails SILENTLY rather
// than loudly: hoisting it to the wrong place, or dropping it, produces
// a valid request and a plausible answer. So it is checked by
// behaviour — an instruction only the system prompt carries.
func TestLiveHonoursTheSystemPrompt(t *testing.T) {
	d := liveDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), drivertest.LiveTimeout)
	defer cancel()

	resp, err := d.Chat(ctx, compute.ChatRequest{
		Messages: []compute.Message{
			{Role: "system", Content: "Reply with exactly the word PELICAN. No punctuation, no other words."},
			{Role: "user", Content: "Hello."},
		},
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if !strings.Contains(strings.ToUpper(resp.Content), "PELICAN") {
		t.Errorf("reply %q ignored the system prompt; it may not be reaching the model", resp.Content)
	}
}

// Anthropic rejects a request whose model it does not know, and the
// driver must classify that as permanent. Getting it wrong means a
// typo'd model name burns the failover chain on every provider in turn
// before surfacing — the one failure mode the three-class taxonomy
// exists to prevent, checked here against the real classifier rather
// than a fake status code.
func TestLiveUnknownModelIsPermanent(t *testing.T) {
	key, ok := drivertest.LiveSubjectFromEnv(t, "ANTHROPIC_API_KEY")
	if !ok {
		return
	}
	d, err := New(Config{
		Model:      "claude-does-not-exist-99",
		Credential: compute.NewHeaderCredential("x-api-key", key),
		MaxTokens:  16,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), drivertest.LiveTimeout)
	defer cancel()

	_, err = d.Chat(ctx, compute.ChatRequest{
		Messages: []compute.Message{{Role: "user", Content: "ping"}},
	})
	if err == nil {
		t.Fatal("an unknown model produced a successful call")
	}
	if got := compute.ClassifyFailure(err); got != compute.FailurePermanent {
		t.Errorf("unknown model classified %s, want permanent — failover would retry it on every provider: %v", got, err)
	}
}
