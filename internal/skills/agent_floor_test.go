package skills

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest is a capability request. Tier-first precedence stops an
// agent taking a name it should not have; it says nothing about what
// an agent-authored skill may DO once it has a name of its own.
//
// Without a floor, a skill the agent wrote for itself could declare a
// credential grant, an egress allowlist, or a binary to fetch and
// execute — each granted by the same machinery that grants them to an
// operator, on the strength of a document the agent wrote.

func agentSkillDir(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	writeHandler(t, dir, "handler.py", "print('hi')")
	writeManifest(t, dir, `
name: helper
version: 1.0.0
runtime: python
handler: handler.py
`+extra)
	return dir
}

func TestAgentSkillCannotDeclareCredentials(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
credentials:
  - provider: github
    scopes: [repo]
`)
	_, err := ParseAgentSkill(dir)
	if !errors.Is(err, ErrAgentTierCapability) {
		t.Fatalf("err = %v, want ErrAgentTierCapability", err)
	}
	if !strings.Contains(err.Error(), "github") {
		t.Errorf("err = %q; it does not name what was asked for", err)
	}
}

func TestAgentSkillCannotDeclareBinaries(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
binaries:
  - name: helper-bin
    url: https://example.com/helper
    sha256: abc123
    target: bin/helper
`)
	_, err := ParseAgentSkill(dir)
	if !errors.Is(err, ErrAgentTierCapability) {
		t.Fatalf("err = %v, want ErrAgentTierCapability", err)
	}
	if !strings.Contains(err.Error(), "helper-bin") {
		t.Errorf("err = %q; it does not name the binary", err)
	}
}

func TestAgentSkillCannotDeclareNetwork(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
network:
  - api.example.com
`)
	_, err := ParseAgentSkill(dir)
	if !errors.Is(err, ErrAgentTierCapability) {
		t.Fatalf("err = %v, want ErrAgentTierCapability", err)
	}
	if !strings.Contains(err.Error(), "api.example.com") {
		t.Errorf("err = %q; it does not name the host", err)
	}
}

func TestAgentSkillCannotRequireHostBinaries(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
requires_binary: [kubectl]
`)
	_, err := ParseAgentSkill(dir)
	if !errors.Is(err, ErrAgentTierCapability) {
		t.Fatalf("err = %v, want ErrAgentTierCapability", err)
	}
	if !strings.Contains(err.Error(), "kubectl") {
		t.Errorf("err = %q; it does not name the binary", err)
	}
}

// Storage is deliberately allowed. A storage declaration is scoped to
// mounts the operator configured, so it cannot reach past what they
// already permitted — and refusing it would make the agent unable to
// write a skill that reads a file, which is most of them.
func TestAgentSkillMayDeclareStorage(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
storage:
  - label: workspace
    mode: read
`)
	skill, err := ParseAgentSkill(dir)
	if err != nil {
		t.Fatalf("an ordinary agent skill was refused: %v", err)
	}
	if skill.Tier != TierAgent {
		t.Errorf("tier = %v, want agent", skill.Tier)
	}
}

// The refusal has to say what the operator can do about it, or the
// only route forward is guesswork.
func TestTheRefusalSaysHowToGrantIt(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, "requires_binary: [jq]\n")
	_, err := ParseAgentSkill(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "operator tier") {
		t.Errorf("err = %q; it does not say how an operator can grant this", err)
	}
}

// Everything asked for at once is reported at once. Reporting the
// first and stopping means fixing a manifest one round trip per
// declaration.
func TestEveryRefusedCapabilityIsListed(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, `
requires_binary: [jq]
network: [api.example.com]
credentials:
  - provider: github
    scopes: [repo]
`)
	_, err := ParseAgentSkill(dir)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"jq", "api.example.com", "github"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q; it does not mention %q", err, want)
		}
	}
}

// The same manifest at the operator tier is fine — the floor caps the
// AGENT, not the capability.
func TestTheSameManifestLoadsAtTheOperatorTier(t *testing.T) {
	t.Parallel()
	dir := agentSkillDir(t, "requires_binary: [jq]\n")
	if _, err := Parse(dir); err != nil {
		t.Errorf("an operator skill was caught by the agent floor: %v", err)
	}
}

// Put enforces it too. A rule applied by one entry point is a rule a
// second entry point silently does not apply.
func TestPutRefusesAnAgentSkillThatWidensCapabilities(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	r := NewRegistry(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})))

	r.Put(&Skill{
		Manifest: Manifest{
			Name: "sneaky", Version: "1.0.0",
			RequiresBinary: []string{"kubectl"},
		},
		ManifestDir: "/cache/self-taught",
		Tier:        TierAgent,
	})

	if _, err := r.Get("sneaky"); err == nil {
		t.Error("an agent skill declaring a host binary was registered")
	}
	if !strings.Contains(logs.String(), "kubectl") {
		t.Errorf("the refusal was silent; log was:\n%s", logs.String())
	}
}

// And an ordinary agent skill still registers, or the floor has just
// disabled the tier.
func TestPutAcceptsAnOrdinaryAgentSkill(t *testing.T) {
	t.Parallel()
	r := NewRegistry(slog.New(slog.DiscardHandler))
	r.Put(&Skill{
		Manifest:    Manifest{Name: "ordinary", Version: "1.0.0"},
		ManifestDir: "/cache/self-taught",
		Tier:        TierAgent,
	})
	if _, err := r.Get("ordinary"); err != nil {
		t.Errorf("an ordinary agent skill was refused: %v", err)
	}
}

// A directory that is not a skill at all must fail as a parse error,
// not as a capability refusal — the two send an operator to different
// places.
func TestAgentParseStillReportsOrdinaryErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("not: valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseAgentSkill(dir)
	if err == nil {
		t.Fatal("a malformed manifest parsed")
	}
	if errors.Is(err, ErrAgentTierCapability) {
		t.Errorf("a malformed manifest was reported as a capability refusal: %v", err)
	}
}
