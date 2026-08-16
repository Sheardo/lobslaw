package compute

import (
	"strings"
	"testing"
)

// A failing command routinely echoes the argument that failed, and
// that argument is sometimes the key. Without redaction the secret
// lands in the transcript, gets replayed on every later turn, and can
// be summarised into memory — one leak becoming permanent.
func TestToolErrorIsRedactedBeforeTheModelSeesIt(t *testing.T) {
	t.Parallel()

	msg := toolResultMessage(ToolCall{ID: "c1", Name: "shell"}, ToolInvocation{
		Error: "git push failed: bad credentials for ghp_abcdefghijklmnopqrstuvwxyz012345",
	})

	if strings.Contains(msg.Content, "ghp_abcdefghijklmnopqrstuvwxyz012345") {
		t.Errorf("the token reached the model verbatim:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "[redacted]") {
		t.Errorf("no redaction marker:\n%s", msg.Content)
	}
	// The diagnostic has to survive, or redaction costs the model the
	// information it needs to recover.
	if !strings.Contains(msg.Content, "git push failed") {
		t.Errorf("redaction ate the error text:\n%s", msg.Content)
	}
	if msg.Role != "tool" || msg.ToolCallID != "c1" {
		t.Errorf("message shape changed: role=%q id=%q", msg.Role, msg.ToolCallID)
	}
}

// Output as well as errors: a tool that cats a config file is the
// more likely leak of the two.
func TestToolOutputIsRedacted(t *testing.T) {
	t.Parallel()

	msg := toolResultMessage(ToolCall{ID: "c2", Name: "read_file"}, ToolInvocation{
		Output: "OPENAI_KEY=sk-abcdefghijklmnopqrstuvwx\nDEBUG=true",
	})

	if strings.Contains(msg.Content, "sk-abcdefghijklmnopqrstuvwx") {
		t.Errorf("a key in tool output reached the model:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "DEBUG=true") {
		t.Errorf("redaction removed non-secret output:\n%s", msg.Content)
	}
}

// Ordinary output must pass through untouched — a redactor that
// mangles normal text is worse than none.
func TestOrdinaryToolOutputIsUnchanged(t *testing.T) {
	t.Parallel()
	const out = "3 files changed, 12 insertions(+), 4 deletions(-)"
	msg := toolResultMessage(ToolCall{ID: "c3", Name: "shell"}, ToolInvocation{Output: out})
	if !strings.Contains(msg.Content, out) {
		t.Errorf("ordinary output was altered:\n%s", msg.Content)
	}
	if strings.Contains(msg.Content, "[redacted]") {
		t.Errorf("redaction fired on clean output:\n%s", msg.Content)
	}
}
