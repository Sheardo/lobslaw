package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Every manifest before the prose runtime had to name a handler, which
// encoded an assumption that turns out to be wrong: that a skill is a
// program. Most of what the agent teaches itself is procedure in
// prose, and inventing a no-op handler so the type-check passes would
// be a lie the invoker would then try to run.

func TestAProseSkillNeedsNoHandler(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: how-to-review
version: 1.0.0
runtime: prose
description: how this user likes code reviewed
`)
	skill, err := Parse(dir)
	if err != nil {
		t.Fatalf("a prose skill was refused: %v", err)
	}
	if skill.HandlerPath != "" {
		t.Errorf("HandlerPath = %q, want empty — a non-empty one is a path something may try to exec",
			skill.HandlerPath)
	}
	if skill.Manifest.Runtime.Executable() {
		t.Error("the prose runtime reports itself executable")
	}
}

// The two halves of the manifest must not be able to disagree: a
// manifest saying "there is nothing to run" while pointing at a script
// is one whose meaning depends on which code path reads it first.
func TestAProseSkillMayNotNameAHandler(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHandler(t, dir, "handler.py", "print('hi')")
	writeManifest(t, dir, `
name: confused
version: 1.0.0
runtime: prose
handler: handler.py
`)
	_, err := Parse(dir)
	if err == nil {
		t.Fatal("a prose manifest naming a handler parsed")
	}
	if !strings.Contains(err.Error(), "runs nothing") {
		t.Errorf("err = %q; it does not explain the contradiction", err)
	}
}

// A digest pinning a file that does not exist reads, to anybody
// auditing, as a skill whose code is pinned.
func TestAProseSkillMayNotPinAHandlerDigest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: confused
version: 1.0.0
runtime: prose
handler_sha256: abc123
`)
	if _, err := Parse(dir); err == nil {
		t.Fatal("a prose manifest pinning a handler digest parsed")
	}
}

// And the rule the prose runtime relaxes still applies to every other
// runtime — this is the check that the change did not just make
// handler optional everywhere.
func TestAnExecutableSkillStillRequiresAHandler(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: still-required
version: 1.0.0
runtime: python
`)
	_, err := Parse(dir)
	if err == nil {
		t.Fatal("a python manifest with no handler parsed")
	}
	if !strings.Contains(err.Error(), "handler is required") {
		t.Errorf("err = %q", err)
	}
}

// Refused at the top of Invoke rather than falling through to
// "unsupported runtime" from the interpreter lookup. That error reads
// as a misconfiguration; this is not one, and the difference decides
// whether somebody goes looking for a broken install.
func TestInvokingAProseSkillIsRefusedWithItsOwnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: how-to-review
version: 1.0.0
runtime: prose
`)
	skill, err := Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(nil)
	r.Put(skill)

	inv, err := NewInvoker(InvokerConfig{Registry: r})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inv.Invoke(context.Background(), InvokeRequest{SkillName: "how-to-review"})
	if !errors.Is(err, ErrNotExecutable) {
		t.Fatalf("err = %v, want ErrNotExecutable", err)
	}
	if !strings.Contains(err.Error(), "read its body") {
		t.Errorf("err = %q; it does not say what to do instead", err)
	}
}

// The prose runtime does not become an escape from the tier floor: an
// agent-authored prose skill still cannot ask for a credential.
func TestAProseSkillIsStillCappedAtTheAgentTier(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: sneaky
version: 1.0.0
runtime: prose
credentials:
  - provider: github
    scopes: [repo]
`)
	if _, err := ParseAgentSkill(dir); !errors.Is(err, ErrAgentTierCapability) {
		t.Fatalf("err = %v, want ErrAgentTierCapability", err)
	}
}

func TestAnUnknownRuntimeStillFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, `
name: typo
version: 1.0.0
runtime: prosee
`)
	if _, err := Parse(dir); err == nil {
		t.Fatal("a typo'd runtime parsed")
	}
}
