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

// Precedence is tier-first precisely so a version bump cannot promote
// a skill past its provenance. That leaves a real problem: a signed
// skill misbehaves, the operator has a fix, and there is no way to try
// it — bumping the version no longer wins, which is exactly what
// tier-first was for.
//
// So the escape hatch is a separate SOURCE rather than a way to game
// the order, and it is deliberately awkward to leave on by accident.

func devSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHandler(t, dir, "handler.py", "print('dev')")
	writeManifest(t, dir, `
name: `+name+`
version: 0.0.1
runtime: python
handler: handler.py
`)
	return dir
}

// --- the two gates ---------------------------------------------------

// Configured without the marker is a REFUSAL, not a silent skip. An
// operator who configured it and had it ignored would develop against
// a skill that was never loaded; one who left it in a production
// config would run an unsigned override without knowing.
func TestADevSourceWithoutTheMarkerRefuses(t *testing.T) {
	t.Parallel()
	err := CheckDevSource("/some/dev/skills", "")
	if !errors.Is(err, ErrDevSourceUngated) {
		t.Fatalf("err = %v, want ErrDevSourceUngated", err)
	}
	// Names BOTH halves: somebody hitting this in production needs to
	// know which setting to remove, somebody in development needs to
	// know what to export.
	if !strings.Contains(err.Error(), "/some/dev/skills") {
		t.Errorf("err = %q; it does not name the directory", err)
	}
	if !strings.Contains(err.Error(), DevMarkerEnv) {
		t.Errorf("err = %q; it does not name the marker", err)
	}
	// And says what it would do, so the refusal is not merely
	// bureaucratic.
	if !strings.Contains(err.Error(), "outrank every signed skill") {
		t.Errorf("err = %q; it does not say why this is gated", err)
	}
}

// No dev source configured is every deployment, and must be silent.
func TestNoDevSourceIsFine(t *testing.T) {
	t.Parallel()
	if err := CheckDevSource("", ""); err != nil {
		t.Errorf("an unconfigured dev source errored: %v", err)
	}
	if err := CheckDevSource("   ", ""); err != nil {
		t.Errorf("a blank dev source errored: %v", err)
	}
}

func TestAGatedDevSourceIsAccepted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := CheckDevSource(dir, "1"); err != nil {
		t.Errorf("a gated dev source was refused: %v", err)
	}
}

// A typo'd path loads nothing and looks exactly like a directory whose
// skills all failed to parse. The operator would spend the difference
// debugging the wrong one.
func TestAMissingDevDirectoryIsRefused(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "typo")
	err := CheckDevSource(missing, "1")
	if err == nil {
		t.Fatal("a dev source pointing at nothing was accepted")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("err = %q; it does not name the path", err)
	}
}

func TestAFileIsNotADevSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDevSource(path, "1"); err == nil {
		t.Error("a regular file was accepted as a dev source")
	}
}

func TestARelativeDevSourceIsRefused(t *testing.T) {
	t.Parallel()
	if err := CheckDevSource("./skills", "1"); err == nil {
		t.Error("a relative dev source was accepted")
	}
}

// --- what it does ------------------------------------------------------

// The whole point: it beats a SIGNED skill, which nothing else does.
func TestADevSkillOutranksASignedOne(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	devDir := devSkill(t, root, "tidy")

	signedDir := t.TempDir()
	writeHandler(t, signedDir, "handler.py", "print('published')")
	writeManifest(t, signedDir, `
name: tidy
version: 9.9.9
runtime: python
handler: handler.py
`)
	published, err := Parse(signedDir)
	if err != nil {
		t.Fatal(err)
	}
	// Signed AND a far higher version — the two things that normally
	// win. Neither should.
	published.IsSigned = true
	published.Tier = TierSigned

	r := NewRegistry(slog.New(slog.DiscardHandler))
	r.Put(published)
	if errs := r.ScanDev(root); len(errs) != 0 {
		t.Fatal(errs)
	}

	got, err := r.Get("tidy")
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestDir != devDir {
		t.Errorf("winner = %q, want the dev skill at %q", got.ManifestDir, devDir)
	}
	if got.Tier != TierDev {
		t.Errorf("tier = %v, want dev", got.Tier)
	}
}

// Set explicitly, not derived. tierOf returns TierOperator for an
// unsigned skill, and the whole point of this source is that it beats
// a signed one.
func TestADevSkillIsTaggedExplicitly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	devSkill(t, root, "tidy")
	r := NewRegistry(slog.New(slog.DiscardHandler))
	if errs := r.ScanDev(root); len(errs) != 0 {
		t.Fatal(errs)
	}
	got, err := r.Get("tidy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierDev {
		t.Fatalf("tier = %v; it was derived rather than set", got.Tier)
	}
	if tierOf(got) != TierDev {
		t.Errorf("effective tier = %v", tierOf(got))
	}
}

// A dev skill overriding something is a WARNING, every time. It is a
// state the operator should be reminded they are in.
func TestADevOverrideIsWarnedAbout(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	root := t.TempDir()
	devSkill(t, root, "tidy")
	r := NewRegistry(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if errs := r.ScanDev(root); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !strings.Contains(logs.String(), "DEV skill") {
		t.Errorf("a dev override was silent:\n%s", logs.String())
	}
}

// Never signature-checked. A dev skill is by definition not the
// published one, and demanding a signature would make the escape hatch
// useless in exactly the case it exists for.
func TestADevSkillNeedsNoSignature(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	devSkill(t, root, "tidy")
	r := NewRegistry(slog.New(slog.DiscardHandler))
	if errs := r.ScanDev(root); len(errs) != 0 {
		t.Fatalf("an unsigned dev skill was refused: %v", errs)
	}
	if _, err := r.Get("tidy"); err != nil {
		t.Errorf("the dev skill did not register: %v", err)
	}
}

// One level, not two. A dev source is a working directory somebody
// edits by hand, and making them mint version subdirectories to try a
// change would defeat the purpose.
func TestTheDevLayoutIsOneLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	devSkill(t, root, "tidy")
	r := NewRegistry(slog.New(slog.DiscardHandler))
	if errs := r.ScanDev(root); len(errs) != 0 {
		t.Fatal(errs)
	}
	got, err := r.Get("tidy")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.ManifestDir) != "tidy" {
		t.Errorf("dir = %q; the scan expected a version subdirectory", got.ManifestDir)
	}
}

func TestScanningAMissingDevRootIsSilent(t *testing.T) {
	t.Parallel()
	r := NewRegistry(slog.New(slog.DiscardHandler))
	if errs := r.ScanDev(filepath.Join(t.TempDir(), "gone")); len(errs) != 0 {
		t.Errorf("errs = %v", errs)
	}
}
