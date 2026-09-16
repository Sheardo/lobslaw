package compute_test

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// R33 found three implementations of "run an agent turn" —
// runTaskAsAgentTurn, runCommitmentAsAgentTurn and the research worker
// path — and named the fix as belonging in the writing rather than the
// post-mortem: one turn-runner, N callers.
//
// compute.TurnRunner is that writing. This test is what keeps it true,
// because the failure mode is not a bug anyone would notice: a fourth
// copy works perfectly, and only drifts later, one forgotten budget or
// one missing bot profile at a time.
//
// The allowlist below is deliberately small and annotated. Adding to
// it is allowed — it just has to be a decision somebody wrote down,
// rather than a call site that slipped in.
var allowedDirectLoopCallers = map[string]string{
	// The runner itself. This is the one place.
	"internal/compute/turnrunner.go": "the turn runner",

	// The agent's own definition of the method.
	"internal/compute/agent.go": "declares RunToolCallLoop",

	// Channel handlers are a genuinely different shape: a human is
	// waiting, there is a session to write, a responder to drive and a
	// confirmation to route back to the person who asked. They are not
	// headless turns and folding them in would mean the runner grew
	// every one of those concerns.
	"internal/gateway/rest.go":       "REST channel: session + responder + prompt routing",
	"internal/gateway/telegram.go":   "Telegram channel: session + responder + prompt routing",
	"internal/gateway/slack_turn.go": "Slack channel: session + responder + prompt routing",
	"internal/gateway/webhook.go":    "webhook channel: inbound HTTP turn",
}

func TestOnlyOneImplementationOfRunningAHeadlessTurn(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var found []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "web" || name == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(string(src), "RunToolCallLoop(") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no callers at all — the scan is broken, not the code")
	}

	var unexpected []string
	for _, rel := range found {
		if _, ok := allowedDirectLoopCallers[rel]; !ok {
			unexpected = append(unexpected, rel)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("new direct caller(s) of RunToolCallLoop: %v\n\n"+
			"A headless turn — a scheduled routine, a commitment, a research worker, an\n"+
			"inbox item, a delegated question — goes through compute.TurnRunner. It owns\n"+
			"budget derivation, bot-profile resolution and the structural tool filter, and\n"+
			"a call site that skips it silently skips all three.\n\n"+
			"If this really is a new channel handler, add it to allowedDirectLoopCallers\n"+
			"with a one-line reason.", unexpected)
	}

	// The allowlist must not outlive what it describes. A stale entry
	// is how a list like this stops meaning anything.
	for rel := range allowedDirectLoopCallers {
		if !slices.Contains(found, rel) {
			t.Errorf("allowedDirectLoopCallers names %q, which no longer calls RunToolCallLoop; remove it", rel)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repo root")
		}
		dir = parent
	}
}
