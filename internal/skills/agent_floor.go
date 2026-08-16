package skills

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The capability floor for agent-authored skills.
//
// Tier-first precedence stops an agent taking a name it should not
// have. It says nothing about what an agent-authored skill may DO once
// it has a name of its own, and that is the larger question: a manifest
// is a capability request, so a skill the agent wrote for itself could
// otherwise declare a credential grant, a network allowlist, or a
// binary to fetch and execute — and every one of those would be
// granted by the same machinery that grants them to an operator, on
// the strength of a document the agent wrote.
//
// So the agent tier is capped at what it can already do. Nothing here
// grants; it only refuses to widen.
//
// Refused at LOAD rather than at invoke, and loudly. A skill that
// silently loses half its manifest would fail later in a way nobody
// can trace back to this decision — and a skill that asked for a
// credential and did not get one is not a working skill with a smaller
// blast radius, it is a broken one pretending.

// ErrAgentTierCapability is returned when an agent-authored skill
// asks for a capability its tier cannot hold.
var ErrAgentTierCapability = errors.New("skills: an agent-authored skill cannot declare this")

// checkAgentFloor refuses an agent-tier manifest that widens the
// deployment's capability surface.
//
// The four it refuses are the ones that reach OUTSIDE the sandbox the
// skill already runs in:
//
//   - credentials: a grant to act as somebody, at a third party
//   - binaries: fetch and execute code from a URL
//   - network: an egress allowlist
//   - requires_binary: a host binary the operator may not have chosen
//
// Storage is deliberately NOT on the list. A storage declaration is
// scoped to mounts the operator configured, so it cannot reach past
// what they already permitted — and refusing it would make the agent
// unable to write a skill that reads a file, which is most of them.
func checkAgentFloor(m *Manifest) error {
	var asked []string

	if len(m.Credentials) > 0 {
		providers := make([]string, 0, len(m.Credentials))
		for _, c := range m.Credentials {
			providers = append(providers, c.Provider)
		}
		sort.Strings(providers)
		asked = append(asked, fmt.Sprintf("credentials for %s", strings.Join(providers, ", ")))
	}
	if len(m.Binaries) > 0 {
		names := make([]string, 0, len(m.Binaries))
		for _, b := range m.Binaries {
			names = append(names, b.Name)
		}
		sort.Strings(names)
		asked = append(asked, fmt.Sprintf("binaries to download and execute (%s)", strings.Join(names, ", ")))
	}
	if len(m.Network) > 0 {
		hosts := append([]string(nil), m.Network...)
		sort.Strings(hosts)
		asked = append(asked, fmt.Sprintf("network access to %s", strings.Join(hosts, ", ")))
	}
	if len(m.RequiresBinary) > 0 {
		names := append([]string(nil), m.RequiresBinary...)
		sort.Strings(names)
		asked = append(asked, fmt.Sprintf("host binaries %s", strings.Join(names, ", ")))
	}

	if len(asked) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %q asks for %s. An operator can grant these by adopting the skill — copy it into "+
			"the skills directory and it loads at the operator tier",
		ErrAgentTierCapability, m.Name, strings.Join(asked, "; "))
}
