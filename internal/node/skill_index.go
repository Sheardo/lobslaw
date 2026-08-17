package node

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
				References:  skills.ReferencePaths(s.Manifest.References),
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

// skillDocs serves levels 1 and 2 from the registry.
//
// READ AT CALL TIME, NOT CACHED AT BOOT. A skill's documents live
// beside its manifest and are re-materialised whenever the store
// changes; a copy taken at start-up would serve an operator the
// instructions for a version they replaced this morning.
//
// The digests recorded at parse time are re-checked here for the same
// reason the invoker re-hashes the handler before exec: a document
// swapped after registration is exactly the substitution the digest
// exists to catch, and instructions steer what the agent does as
// surely as code does.
type skillDocs struct {
	reg *skills.Registry
	log *slog.Logger
}

func (n *Node) skillDocs() *skillDocs {
	if n.skillRegistry == nil {
		return nil
	}
	return &skillDocs{reg: n.skillRegistry, log: n.log}
}

func (d *skillDocs) Has(name string) bool {
	if d == nil || d.reg == nil {
		return false
	}
	_, err := d.reg.Get(name)
	return err == nil
}

func (d *skillDocs) Body(name string) (string, bool) {
	s, err := d.lookup(name)
	if err != nil || strings.TrimSpace(s.Manifest.Body) == "" {
		return "", false
	}
	return d.read(s, s.Manifest.Body, s.BodySHA256)
}

func (d *skillDocs) Reference(name, path string) (string, bool) {
	s, err := d.lookup(name)
	if err != nil {
		return "", false
	}
	// Only a path the manifest DECLARED. Without this the agent could
	// read any file beside the manifest by naming it, which is a
	// directory listing dressed as documentation.
	for _, r := range s.Manifest.References {
		if r.Path == path {
			return d.read(s, path, s.ReferenceSHA256[path])
		}
	}
	return "", false
}

func (d *skillDocs) lookup(name string) (*skills.Skill, error) {
	if d == nil || d.reg == nil {
		return nil, errors.New("no skill registry")
	}
	return d.reg.Get(name)
}

// read loads one document, refusing it if its digest has moved.
func (d *skillDocs) read(s *skills.Skill, rel, wantDigest string) (string, bool) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		d.log.Warn("skills: refusing a document path outside the skill directory",
			"skill", s.Manifest.Name, "path", rel)
		return "", false
	}
	full := filepath.Join(s.ManifestDir, clean)
	raw, err := os.ReadFile(full)
	if err != nil {
		d.log.Warn("skills: document declared but unreadable",
			"skill", s.Manifest.Name, "path", rel, "err", err)
		return "", false
	}
	if wantDigest != "" {
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, wantDigest) {
			// Refused rather than served with a warning. A document
			// that changed after registration is the substitution the
			// digest exists to catch, and serving it "with a caveat"
			// puts the caveat in a log nobody reads and the
			// instructions in the model's context.
			d.log.Error("skills: document does not match its verified digest; refusing to serve it",
				"skill", s.Manifest.Name, "path", rel)
			return "", false
		}
	}
	return string(raw), true
}
