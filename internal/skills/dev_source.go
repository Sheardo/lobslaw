package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The dev source: a directory that outranks everything, including
// signed skills.
//
// Precedence is tier-first precisely so a version bump cannot promote
// a skill past its provenance. That leaves an operator with a real
// problem: a signed skill is misbehaving, they have a fix, and there
// is no way to try it — bumping the version no longer wins, which is
// exactly what tier-first was for.
//
// So the escape hatch is a separate SOURCE rather than a way to game
// the order. It is deliberately awkward to leave on by accident.

// DevMarkerEnv must be set in the process environment for a dev source
// to be honoured.
//
// Two gates — a config key AND an environment variable — because
// either alone is easy to leave behind. A config file gets copied to
// production wholesale; an environment variable gets set in a shell
// profile and forgotten. Both at once is a coincidence somebody has to
// arrange.
const DevMarkerEnv = "LOBSLAW_DEV"

// ErrDevSourceUngated is returned when a dev source is configured
// without the marker.
var ErrDevSourceUngated = fmt.Errorf("skills: dev source is configured but %s is not set", DevMarkerEnv)

// CheckDevSource validates a configured dev source.
//
// Returns an error the caller should treat as FATAL. Refusing to boot
// is the answer rather than ignoring the setting: an operator who
// configured a dev source and had it silently skipped would develop
// against a skill that was never loaded, and one who left it in a
// production config would be running an unsigned override without
// knowing. Neither is a state to start in.
//
// The error names both halves — the directory and the missing marker —
// because somebody hitting it in production needs to know which
// setting to remove, and somebody hitting it in development needs to
// know what to export.
func CheckDevSource(dir string, marker string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if strings.TrimSpace(marker) == "" {
		return fmt.Errorf(
			"%w: skills.dev_source = %q would outrank every signed skill on this node. "+
				"Set %s=1 to develop against it, or remove the setting",
			ErrDevSourceUngated, dir, DevMarkerEnv)
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("skills: dev_source %q must be an absolute path", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		// Refused rather than treated as empty. A dev source pointing
		// at a typo'd path loads nothing and looks exactly like a dev
		// source pointing at a directory whose skills all failed to
		// parse — and the operator would spend the difference between
		// those two debugging the wrong one.
		return fmt.Errorf("skills: dev_source %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("skills: dev_source %q is not a directory", dir)
	}
	return nil
}

// ScanDev loads a dev source, tagging everything TierDev.
//
// Layout is <root>/<name>/manifest.yaml — one level, like the old
// mount, not the two-level cache layout. A dev source is a working
// directory somebody edits by hand, and making them mint version
// subdirectories to try a change would defeat the purpose.
//
// Never signature-checked. A dev skill is by definition not the
// published one, and demanding a signature would make the escape hatch
// useless in exactly the case it exists for. That is why the gates are
// on the SOURCE rather than on its contents.
func (r *Registry) ScanDev(root string) []error {
	var errs []error
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []error{err}
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		skill, err := ParseWithPolicy(dir, SigningOff, nil)
		if err != nil {
			if _, statErr := os.Stat(filepath.Join(dir, "manifest.yaml")); os.IsNotExist(statErr) {
				continue
			}
			r.log.Warn("skills: dev skill failed to load", "dir", dir, "err", err)
			errs = append(errs, err)
			continue
		}
		// Set explicitly, not derived. tierOf would return TierOperator
		// for an unsigned skill, and the whole point of this source is
		// that it beats a signed one.
		skill.Tier = TierDev
		r.Put(skill)
		r.log.Warn("skills: a DEV skill is overriding by tier",
			"skill", skill.Name(), "version", skill.Manifest.Version, "dir", dir)
	}
	return errs
}
