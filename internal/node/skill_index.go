package node

import (
	"os/exec"
	"sync"

	"github.com/jmylchreest/lobslaw/internal/skills"
	"github.com/jmylchreest/lobslaw/pkg/promptgen"
)

// The skill index is level 0 of progressive disclosure: every
// installed skill, by name and one line, in the system prompt on every
// turn. Bodies are fetched on demand; the index is not.
//
// It has to be COMPLETE. Ranking and showing the top few is the
// obvious optimisation and it is the wrong one — a retrieval miss
// makes a capability invisible and the model then confabulates about
// what it has, which is precisely the failure that killed keyword
// tailoring in this codebase before. Ranking better is not the same as
// not hiding things.
//
// The one thing safely dropped is a skill that could not run here at
// all: wrong platform, missing capability, missing binary. Advertising
// those teaches the model it has a capability it will then fail to
// use, which is worse than not mentioning them.

// skillIndexProvider returns the level-0 index, filtered to what this
// node can actually run.
func (n *Node) skillIndexProvider() func() []promptgen.SkillInfo {
	if n.skillRegistry == nil {
		return nil
	}
	// Reported once per skill rather than per turn. An operator whose
	// skill vanished needs to see why; they do not need to see it
	// every time somebody sends a message.
	var reported sync.Map

	return func() []promptgen.SkillInfo {
		env := skills.Environment{
			Capabilities: n.configuredCapabilities(),
			HasBinary: func(name string) bool {
				_, err := exec.LookPath(name)
				return err == nil
			},
		}

		installed := n.skillRegistry.List()
		out := make([]promptgen.SkillInfo, 0, len(installed))
		for _, s := range installed {
			if ok, why := skills.Applicable(&s.Manifest, env); !ok {
				if _, seen := reported.LoadOrStore(s.Manifest.Name, struct{}{}); !seen {
					n.log.Info("skills: not advertised on this node",
						"skill", s.Manifest.Name, "reason", why)
				}
				continue
			}
			out = append(out, promptgen.SkillInfo{
				Name:        s.Manifest.Name,
				Description: s.Manifest.Description,
				Location:    s.ManifestDir,
				References:  s.Manifest.References,
			})
		}
		return out
	}
}

// configuredCapabilities collects every capability any provider
// declares, so a skill needing "vision" is judged against what the
// deployment actually has rather than against a hardcoded list.
func (n *Node) configuredCapabilities() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range n.cfg.Compute.Providers {
		for _, c := range p.Capabilities {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
