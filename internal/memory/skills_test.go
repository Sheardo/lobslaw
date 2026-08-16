package memory

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Authority moves from the filesystem to the store. The one property
// that decides whether that is possible at all: a SIGNED skill has to
// survive the round trip.
//
// A signature is a detached ed25519 signature over the exact manifest
// bytes. Parse it into a proto and re-serialise on export and the
// bytes change, the signature stops verifying, and every
// SigningRequire deployment rejects a skill that genuinely was signed.

func skillStore(t *testing.T) *SkillStore {
	t.Helper()
	node, fsm := newTestRaft(t)
	s, err := NewSkillStore(node, fsm.Store())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// skillDir writes a skill directory and returns its path.
func skillDir(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const sampleManifest = `name: tidy
version: 1.2.3
runtime: python
handler: handler.py
description: tidies things
`

func importSample(t *testing.T, s *SkillStore, dir string) *lobslawv1.SkillRecord {
	t.Helper()
	rec, err := s.Import(context.Background(), ImportRequest{
		Dir: dir, Name: "tidy", Version: "1.2.3",
		Tier:   lobslawv1.SkillTier_SKILL_TIER_OPERATOR,
		Source: "import:" + dir, ImportedBy: "user:john", Activate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// THE test. A signed skill imported and exported must still verify.
func TestASignedSkillSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Signed over the exact bytes, as the real signer does.
	//
	// Deliberately NOT already-normalised: a comment, an unusual key
	// order and trailing whitespace, all of which a parse-and-re-encode
	// would quietly tidy away. A manifest that happened to survive
	// normalisation would let this test pass while the property it
	// exists for was broken.
	manifest := "# publisher: example.com\nversion: 1.2.3\nname: tidy\n" +
		"runtime: python\nhandler: handler.py   \n"
	sig := ed25519.Sign(priv, []byte(manifest))

	dir := skillDir(t, manifest, map[string]string{"handler.py": "print('hi')"})
	if err := os.WriteFile(filepath.Join(dir, SignatureFile), sig, 0o600); err != nil {
		t.Fatal(err)
	}

	s := skillStore(t)
	importSample(t, s, dir)

	out := t.TempDir()
	if err := s.Export("tidy", "1.2.3", out); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(out, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	gotSig, err := os.ReadFile(filepath.Join(out, SignatureFile))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, got, gotSig) {
		t.Fatal("the signature no longer verifies after a round trip through the store — " +
			"the manifest bytes were not preserved verbatim")
	}
	if string(got) != manifest {
		t.Errorf("manifest changed:\n got %q\nwant %q", got, manifest)
	}
}

// Byte-identical, not merely semantically equal. A re-serialised YAML
// that means the same thing is exactly what breaks the signature.
func TestTheManifestIsStoredVerbatim(t *testing.T) {
	t.Parallel()
	// Comments, unusual key order and trailing whitespace: everything a
	// re-encode would quietly normalise away.
	manifest := "# a comment a parser would drop\nversion: 1.2.3\nname: tidy\nruntime: python\nhandler: handler.py   \n"
	dir := skillDir(t, manifest, map[string]string{"handler.py": "x"})

	s := skillStore(t)
	rec := importSample(t, s, dir)

	if string(rec.GetManifestYaml()) != manifest {
		t.Errorf("stored manifest differs:\n got %q\nwant %q", rec.GetManifestYaml(), manifest)
	}
}

// An unsigned skill imports fine and exports without a stray empty
// signature file — which would make an unsigned skill look signed and
// then fail verification.
func TestAnUnsignedSkillExportsWithoutASignature(t *testing.T) {
	t.Parallel()
	dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "x"})
	s := skillStore(t)
	importSample(t, s, dir)

	out := t.TempDir()
	if err := s.Export("tidy", "1.2.3", out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, SignatureFile)); !os.IsNotExist(err) {
		t.Error("an unsigned skill exported a signature file")
	}
}

// --- files and blobs -------------------------------------------------

// Every file, not a filtered subset. A skill's behaviour can depend on
// any file it ships, and an importer keeping only what it recognised
// would materialise differently from the skill somebody tested.
func TestEveryFileIsStored(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"handler.py":          "print('hi')",
		"references/api.md":   "the api",
		"policy.d/tools.yaml": "rules",
		"data/odd.bin":        "\x00\x01\x02",
	}
	dir := skillDir(t, sampleManifest, files)
	s := skillStore(t)
	importSample(t, s, dir)

	out := t.TempDir()
	if err := s.Export("tidy", "1.2.3", out); err != nil {
		t.Fatal(err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// Content-addressed, so two skills sharing a reference store it once.
func TestIdenticalContentIsStoredOnce(t *testing.T) {
	t.Parallel()
	shared := "the same reference document"
	s := skillStore(t)
	for _, name := range []string{"one", "two"} {
		dir := skillDir(t, sampleManifest, map[string]string{"references/shared.md": shared})
		if _, err := s.Import(context.Background(), ImportRequest{
			Dir: dir, Name: name, Version: "1.0.0",
			Tier: lobslawv1.SkillTier_SKILL_TIER_OPERATOR,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var blobs int
	if err := s.store.ForEach(BucketSkillBlobs, func(string, []byte) error {
		blobs++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if blobs != 1 {
		t.Errorf("%d blobs for one shared document", blobs)
	}
}

// A blob whose bytes no longer hash to its key is corruption.
// Returning it would hand a modified handler to the interpreter with
// the digest still looking right.
func TestACorruptedBlobIsRefused(t *testing.T) {
	t.Parallel()
	dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "print('hi')"})
	s := skillStore(t)
	rec := importSample(t, s, dir)

	digest := rec.GetFiles()["handler.py"]
	if err := s.apply(context.Background(), lobslawv1.LogOp_LOG_OP_PUT, digest,
		&lobslawv1.LogEntry_SkillBlob{SkillBlob: &lobslawv1.SkillBlob{
			Digest: digest, Content: []byte("os.system('rm -rf /')"),
		}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Blob(digest); err == nil {
		t.Error("a blob that does not match its digest was returned")
	}
}

// --- size limits -----------------------------------------------------

// Refused at import, naming the path. The person running the import is
// the only one positioned to fix it, and "too large" without saying
// which file leaves them guessing at a bundle they may not have
// assembled by hand.
func TestAnOversizedFileIsRefusedAndNamed(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	s.SetLimits(100, 10000)
	dir := skillDir(t, sampleManifest, map[string]string{
		"references/small.md": "fine",
		"references/huge.md":  strings.Repeat("x", 500),
	})

	_, err := s.Import(context.Background(), ImportRequest{
		Dir: dir, Name: "tidy", Version: "1.2.3",
		Tier: lobslawv1.SkillTier_SKILL_TIER_OPERATOR,
	})
	if !errors.Is(err, ErrSkillTooLarge) {
		t.Fatalf("err = %v, want ErrSkillTooLarge", err)
	}
	if !strings.Contains(err.Error(), "references/huge.md") {
		t.Errorf("err = %q; it does not name the offending file", err)
	}
	// And nothing was stored: a refused import must not leave half a
	// skill behind.
	if _, err := s.Get("tidy", "1.2.3"); err == nil {
		t.Error("a refused import stored a record")
	}
}

// Several small files can still exceed the total.
func TestTheBundleTotalIsCheckedSeparately(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	s.SetLimits(100, 250)
	dir := skillDir(t, "n", map[string]string{
		"a.md": strings.Repeat("x", 90),
		"b.md": strings.Repeat("x", 90),
		"c.md": strings.Repeat("x", 90),
	})
	if _, err := s.Import(context.Background(), ImportRequest{
		Dir: dir, Name: "tidy", Version: "1.0.0",
	}); !errors.Is(err, ErrSkillTooLarge) {
		t.Errorf("err = %v; three files under the per-file limit exceeded the total", err)
	}
}

// --- active version --------------------------------------------------

// One active version per (name, tier). Two would make the registry's
// winner depend on iteration order.
func TestImportingAVersionDeactivatesThePrevious(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	for _, v := range []string{"1.0.0", "2.0.0"} {
		dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "x"})
		if _, err := s.Import(context.Background(), ImportRequest{
			Dir: dir, Name: "tidy", Version: v,
			Tier: lobslawv1.SkillTier_SKILL_TIER_OPERATOR, Activate: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	active, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("%d active versions: %+v", len(active), active)
	}
	if active[0].GetVersion() != "2.0.0" {
		t.Errorf("active = %s, want 2.0.0", active[0].GetVersion())
	}
}

// A different TIER is a different slot: a signed skill and an operator
// one of the same name are both legitimately in force, and precedence
// decides between them.
func TestTiersHaveIndependentActiveVersions(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	for _, tier := range []lobslawv1.SkillTier{
		lobslawv1.SkillTier_SKILL_TIER_OPERATOR,
		lobslawv1.SkillTier_SKILL_TIER_SIGNED,
	} {
		dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "x"})
		if _, err := s.Import(context.Background(), ImportRequest{
			Dir: dir, Name: "tidy", Version: tier.String(),
			Tier: tier, Activate: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	active, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Errorf("%d active, want one per tier", len(active))
	}
}

// --- refusals ---------------------------------------------------------

func TestADirectoryWithNoManifestIsRefused(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	_, err := s.Import(context.Background(), ImportRequest{
		Dir: t.TempDir(), Name: "tidy", Version: "1.0.0",
	})
	if !errors.Is(err, ErrNoManifest) {
		t.Errorf("err = %v, want ErrNoManifest", err)
	}
}

func TestImportNeedsANameAndVersion(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	dir := skillDir(t, sampleManifest, nil)
	for _, req := range []ImportRequest{
		{Dir: dir, Version: "1.0.0"},
		{Dir: dir, Name: "tidy"},
	} {
		if _, err := s.Import(context.Background(), req); err == nil {
			t.Errorf("%+v was accepted", req)
		}
	}
}

// A record is replicated state that a compromised or buggy importer on
// another node could have written, so trusting its paths on the way
// OUT would make export a way to turn a bad record into arbitrary file
// writes.
func TestExportRefusesAPathOutsideTheDirectory(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "x"})
	importSample(t, s, dir)

	rec, err := s.Get("tidy", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	rec.Files["../escape.sh"] = rec.Files["handler.py"]
	if err := s.put(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := s.Export("tidy", "1.2.3", out); err == nil {
		t.Fatal("a record naming a path outside the directory exported")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.sh")); !os.IsNotExist(err) {
		t.Error("the export wrote outside its directory")
	}
}

func TestGettingAnUnknownSkillIsNotFound(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	if _, err := s.Get("nope", "1.0.0"); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("err = %v, want ErrSkillNotFound", err)
	}
}

func TestRemoveDropsTheRecord(t *testing.T) {
	t.Parallel()
	s := skillStore(t)
	dir := skillDir(t, sampleManifest, map[string]string{"handler.py": "x"})
	importSample(t, s, dir)

	if err := s.Remove(context.Background(), "tidy", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("tidy", "1.2.3"); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("err = %v; the record survived", err)
	}
	if err := s.Remove(context.Background(), "tidy", "1.2.3"); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("removing twice: err = %v", err)
	}
}
