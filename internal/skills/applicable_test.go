package skills

import (
	"runtime"
	"strings"
	"testing"
)

// Two properties, pulling in opposite directions. A skill that could
// not run here must not be advertised, because listing it teaches the
// model it has a capability it will then fail to use. And a skill that
// COULD run must always be listed, because the alternative — ranking
// and showing the top few — makes capabilities invisible and brings
// back the confabulation that killed keyword tailoring.

func TestApplicableByDefault(t *testing.T) {
	t.Parallel()
	ok, why := Applicable(&Manifest{Name: "plain"}, Environment{})
	if !ok {
		t.Errorf("a skill declaring no requirements was dropped: %s", why)
	}
}

func TestPlatformGating(t *testing.T) {
	t.Parallel()
	m := &Manifest{Name: "mac-only", Platforms: []string{"darwin"}}

	if ok, _ := Applicable(m, Environment{GOOS: "darwin"}); !ok {
		t.Error("a darwin skill was dropped on darwin")
	}
	ok, why := Applicable(m, Environment{GOOS: "linux"})
	if ok {
		t.Error("a darwin-only skill was advertised on linux")
	}
	if !strings.Contains(why, "linux") || !strings.Contains(why, "darwin") {
		t.Errorf("reason = %q; an operator cannot tell what mismatched", why)
	}

	// Case-insensitive: "Darwin" in a hand-written manifest is not a
	// different platform.
	if ok, _ := Applicable(&Manifest{Platforms: []string{"Darwin"}},
		Environment{GOOS: "darwin"}); !ok {
		t.Error("platform matching is case-sensitive")
	}
}

func TestEmptyGOOSTakesTheHost(t *testing.T) {
	t.Parallel()
	m := &Manifest{Platforms: []string{runtime.GOOS}}
	if ok, why := Applicable(m, Environment{}); !ok {
		t.Errorf("an empty GOOS did not fall back to the host: %s", why)
	}
}

func TestCapabilityGating(t *testing.T) {
	t.Parallel()
	m := &Manifest{Name: "screenshot-reader", RequiresCapability: []string{"vision"}}

	if ok, _ := Applicable(m, Environment{Capabilities: []string{"chat", "vision"}}); !ok {
		t.Error("a vision skill was dropped on a deployment that has vision")
	}
	ok, why := Applicable(m, Environment{Capabilities: []string{"chat"}})
	if ok {
		t.Error("a vision skill was advertised on a text-only deployment")
	}
	if !strings.Contains(why, "vision") {
		t.Errorf("reason = %q; it does not name the missing capability", why)
	}
}

// Every requirement has to hold. Satisfying one of two is not
// satisfying them.
func TestAllRequirementsMustHold(t *testing.T) {
	t.Parallel()
	m := &Manifest{RequiresCapability: []string{"vision", "audio"}}
	if ok, _ := Applicable(m, Environment{Capabilities: []string{"vision"}}); ok {
		t.Error("a skill needing two capabilities was advertised with one")
	}
}

func TestBinaryGating(t *testing.T) {
	t.Parallel()
	m := &Manifest{RequiresBinary: []string{"ffmpeg"}}

	present := Environment{HasBinary: func(string) bool { return true }}
	if ok, _ := Applicable(m, present); !ok {
		t.Error("a skill was dropped despite its binary being present")
	}

	absent := Environment{HasBinary: func(string) bool { return false }}
	ok, why := Applicable(m, absent)
	if ok {
		t.Error("a skill needing a missing binary was advertised")
	}
	if !strings.Contains(why, "ffmpeg") {
		t.Errorf("reason = %q; it does not name the missing binary", why)
	}
}

// A deployment that cannot answer "is this binary present" must not
// silently hide every skill that asks. Failing open is right here:
// the invoker checks again before it execs, so the cost of being
// wrong is one clear error rather than a capability that vanished.
func TestNilBinaryCheckFailsOpen(t *testing.T) {
	t.Parallel()
	m := &Manifest{RequiresBinary: []string{"definitely-not-installed"}}
	if ok, why := Applicable(m, Environment{}); !ok {
		t.Errorf("a nil binary check hid a skill: %s", why)
	}
}
