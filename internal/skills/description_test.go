package skills

import (
	"strings"
	"testing"
)

// The description is rendered into the system prompt on every turn and
// the index is O(skills), so one verbose entry taxes every
// conversation the deployment ever has.
//
// Enforced at PARSE rather than truncated at render. Truncating means
// an operator writes a 200-character description, sees it accepted,
// and silently loses most of it — the error belongs where it can be
// fixed, naming the manifest.

func manifestWith(t *testing.T, extra string) (*Skill, error) {
	t.Helper()
	dir := t.TempDir()
	writeHandler(t, dir, "handler.py", "print('hi')")
	writeManifest(t, dir, `
name: greeter
version: 1.0.0
runtime: python
handler: handler.py
`+extra)
	return Parse(dir)
}

func TestOverlongDescriptionFailsAtParse(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", MaxDescriptionChars+1)
	_, err := manifestWith(t, "description: "+long+"\n")
	if err == nil {
		t.Fatal("an over-long description was accepted and would be silently truncated later")
	}
	if !strings.Contains(err.Error(), "manifest.yaml") {
		t.Errorf("error %q does not name the offending manifest", err)
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error %q does not say which field is wrong", err)
	}
}

func TestDescriptionAtTheLimitIsAccepted(t *testing.T) {
	t.Parallel()
	if _, err := manifestWith(t, "description: "+strings.Repeat("x", MaxDescriptionChars)+"\n"); err != nil {
		t.Errorf("a description exactly at the limit was rejected: %v", err)
	}
}

// Counted in runes, not bytes. An operator writing a description in a
// non-Latin script would otherwise get a limit a third of the one
// documented.
func TestDescriptionLimitCountsRunes(t *testing.T) {
	t.Parallel()
	// Three bytes each, so this is well over the byte limit and
	// exactly at the rune limit.
	if _, err := manifestWith(t, "description: "+strings.Repeat("あ", MaxDescriptionChars)+"\n"); err != nil {
		t.Errorf("a multi-byte description at the rune limit was rejected: %v", err)
	}
}

// The index renders one line per skill, so an embedded newline breaks
// the shape of every entry after it.
func TestMultilineDescriptionIsRejected(t *testing.T) {
	t.Parallel()
	_, err := manifestWith(t, "description: \"first line\\nsecond line\"\n")
	if err == nil {
		t.Fatal("a multi-line description was accepted")
	}
	if !strings.Contains(err.Error(), "single line") {
		t.Errorf("error %q does not explain the constraint", err)
	}
}

// References name bundled documents. A path escaping the skill
// directory would let the index advertise — and a later reader fetch —
// something outside the bundle.
func TestReferencesMustStayInsideTheSkill(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd"} {
		if _, err := manifestWith(t, "references:\n  - \""+bad+"\"\n"); err == nil {
			t.Errorf("reference %q escaped the skill directory unchallenged", bad)
		}
	}
	if _, err := manifestWith(t, "references:\n  - references/api.md\n  - templates/report.md\n"); err != nil {
		t.Errorf("ordinary references were rejected: %v", err)
	}
}

func TestGatingFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	skill, err := manifestWith(t, `platforms: [darwin, linux]
requires_capability: [vision]
references: [references/api.md]
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(skill.Manifest.Platforms) != 2 {
		t.Errorf("platforms = %v", skill.Manifest.Platforms)
	}
	if len(skill.Manifest.RequiresCapability) != 1 || skill.Manifest.RequiresCapability[0] != "vision" {
		t.Errorf("requires_capability = %v", skill.Manifest.RequiresCapability)
	}
	if len(skill.Manifest.References) != 1 {
		t.Errorf("references = %v", skill.Manifest.References)
	}
}
