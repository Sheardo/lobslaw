package skills

import (
	"fmt"
	"runtime"
	"strings"
)

// The skill index is rendered into the system prompt on every turn and
// grows with the library. Once skills install themselves, "listing
// everything is cheap" stops being true on a timeline nobody controls.
//
// The answer is NOT to rank and show the top few. A retrieval miss
// makes a capability invisible and the model confabulates about what
// it has — the exact failure that killed keyword tailoring here
// before. Ranking better is not the same as not hiding things.
//
// What can be dropped safely is a skill that could not run if it were
// chosen. A macOS-only skill on Linux, or one needing a vision
// provider on a text-only deployment, is not a skill the model is
// missing out on — advertising it teaches the model it has a
// capability it will then fail to use, which is worse than silence.

// Environment is what a skill is judged applicable against.
type Environment struct {
	// GOOS is the platform. Empty takes runtime.GOOS, which is what
	// production wants; tests set it explicitly.
	GOOS string

	// Capabilities are the provider capabilities this deployment has
	// ("vision", "audio", …), from [[compute.providers]].
	Capabilities []string

	// HasBinary reports whether a host binary resolves. Nil skips the
	// binary check rather than failing it: a deployment that cannot
	// answer the question should not silently hide every skill that
	// asks it.
	HasBinary func(name string) bool
}

// Applicable reports whether a skill could run here, and why not when
// it could not.
//
// The reason is returned rather than logged because it is what an
// operator asking "where did my skill go" needs, and a skill vanishing
// from the index with no explanation is indistinguishable from one
// that failed to parse.
func Applicable(m *Manifest, env Environment) (bool, string) {
	goos := env.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	if len(m.Platforms) > 0 && !containsFold(m.Platforms, goos) {
		return false, fmt.Sprintf("declares platforms %v; this node is %s",
			m.Platforms, goos)
	}

	for _, want := range m.RequiresCapability {
		if !containsFold(env.Capabilities, want) {
			return false, fmt.Sprintf("requires the %q capability, which no configured provider offers", want)
		}
	}

	if env.HasBinary != nil {
		for _, want := range m.RequiresBinary {
			if !env.HasBinary(want) {
				return false, fmt.Sprintf("requires the %q binary, which is not on PATH", want)
			}
		}
	}

	return true, ""
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}
