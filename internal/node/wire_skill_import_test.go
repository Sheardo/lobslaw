package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/skills"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
)

// The mount stops being a live source and becomes an IMPORT source.
// Files were playing three roles pointed at one directory, and a
// reconciliation rule between two authorities is the complexity R18
// calls self-inflicted.
//
// The consequence an operator has to know about: deleting the file no
// longer removes the skill.

func importerFixture(t *testing.T) (*skillImporter, *memory.SkillStore, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(dir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	// A real raft and a real FSM, because the import path's whole job
	// is to write records somebody else reads back — a stub applier
	// would exercise the marshalling and none of the round trip.
	_, inmem := raft.NewInmemTransport("import-node")
	node, err := memory.NewRaft(memory.RaftConfig{
		NodeID: "import-node", LocalAddr: "import-node",
		DataDir: dir, Bootstrap: true, Transport: inmem,
	}, memory.NewFSM(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Shutdown()
		_ = store.Close()
	})
	if err := node.WaitForLeader(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	skillStore, err := memory.NewSkillStore(node, store)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	root := t.TempDir()
	n := &Node{
		log:                slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		skillStore:         skillStore,
		skillSigningPolicy: skills.SigningOff,
	}
	return &skillImporter{node: n, root: root, label: "skills"}, skillStore, &logs, root
}

// mountSkill writes a skill directory under the mount root.
func mountSkill(t *testing.T, root, dirName, name, version, handler string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(handler))
	manifest := "name: " + name + "\nversion: " + version + "\n" +
		"runtime: python\nhandler: handler.py\n" +
		"handler_sha256: " + hex.EncodeToString(sum[:]) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestASkillDroppedInTheMountReachesTheStore(t *testing.T) {
	t.Parallel()
	imp, store, _, root := importerFixture(t)
	mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")

	if err := imp.importAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Get("tidy", "1.0.0")
	if err != nil {
		t.Fatalf("the skill did not reach the store: %v", err)
	}
	if !rec.GetActive() {
		t.Error("the imported skill is not active")
	}
	if !strings.Contains(rec.GetSource(), "mount:skills") {
		t.Errorf("source = %q; it does not record where it came from", rec.GetSource())
	}
}

// Without this the watcher re-puts every skill on every filesystem
// event, replicating unchanged records to every node for the rest of
// the deployment's life.
func TestAnUnchangedSkillIsNotReimported(t *testing.T) {
	t.Parallel()
	imp, store, _, root := importerFixture(t)
	mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")
	ctx := context.Background()

	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := store.Get("tidy", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := imp.importAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	again, err := store.Get("tidy", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	// The revision is the FSM's write counter, so an unchanged import
	// that still wrote would show here.
	if again.GetRevision() != first.GetRevision() {
		t.Errorf("revision moved from %d to %d across five no-op imports",
			first.GetRevision(), again.GetRevision())
	}
}

// A skill edited in place without its version moving is exactly what a
// drop-in directory is FOR during development. A version check would
// make those edits invisible.
func TestAnEditWithoutAVersionBumpIsPickedUp(t *testing.T) {
	t.Parallel()
	imp, store, _, root := importerFixture(t)
	mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")
	ctx := context.Background()
	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}

	// Same version, different handler.
	mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('goodbye')")
	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := store.Get("tidy", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.Blob(rec.GetFiles()["handler.py"])
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "print('goodbye')" {
		t.Errorf("handler = %q; an in-place edit was not imported", content)
	}
}

// --- the surprising bit ----------------------------------------------

// Deleting the file no longer removes the skill. Said out loud, once,
// rather than discovered when somebody deletes a directory and the
// skill keeps answering.
func TestDeletingTheDirectoryLeavesTheSkillAndSaysSo(t *testing.T) {
	t.Parallel()
	imp, store, logs, root := importerFixture(t)
	dir := mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")
	ctx := context.Background()
	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get("tidy", "1.0.0"); err != nil {
		t.Errorf("deleting the file removed the skill: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "source directory is gone but the skill remains") {
		t.Fatalf("the removal was silent:\n%s", out)
	}
	// And it says how to actually remove it, or the operator is told
	// about a problem with no way out of it.
	if !strings.Contains(out, "lobslaw skills remove") {
		t.Errorf("the notice does not say how to remove it:\n%s", out)
	}
}

// Once per skill per process. A notice repeated on every filesystem
// event is one an operator filters out.
func TestTheRemovalNoticeIsSaidOnce(t *testing.T) {
	t.Parallel()
	imp, _, logs, root := importerFixture(t)
	dir := mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")
	ctx := context.Background()
	if err := imp.importAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err := imp.importAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "source directory is gone"); n != 1 {
		t.Errorf("the notice fired %d times", n)
	}
}

// --- refusals ---------------------------------------------------------

// An unsigned skill in a SigningRequire deployment is refused HERE
// rather than replicated to every node and then failing to load on all
// of them. The feedback belongs where the file was dropped.
func TestAnUnsignedSkillIsRefusedUnderSigningRequire(t *testing.T) {
	t.Parallel()
	imp, store, logs, root := importerFixture(t)
	imp.node.skillSigningPolicy = skills.SigningRequire
	imp.node.skillVerifier = skills.NewVerifier()
	mountSkill(t, root, "tidy", "tidy", "1.0.0", "print('hi')")

	if err := imp.importAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("tidy", "1.0.0"); err == nil {
		t.Error("an unsigned skill was imported under signing_policy=require")
	}
	if !strings.Contains(logs.String(), "could not import") {
		t.Errorf("the refusal was silent:\n%s", logs.String())
	}
}

// A directory that is not a skill is skipped, not reported. A mount
// holding a README is not a problem to warn about on every tick.
func TestANonSkillDirectoryIsSkippedQuietly(t *testing.T) {
	t.Parallel()
	imp, _, logs, root := importerFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := imp.importAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "could not import") {
		t.Errorf("a non-skill directory was reported:\n%s", logs.String())
	}
}

// A configured mount that is not materialised yet is the normal state
// on a node that has just started, not an error to report every tick.
func TestAMissingMountIsNotAnError(t *testing.T) {
	t.Parallel()
	imp, _, _, root := importerFixture(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := imp.importAll(context.Background()); err != nil {
		t.Errorf("a missing mount errored: %v", err)
	}
}

// A malformed manifest is reported, because unlike a README it is
// something somebody meant to be a skill.
func TestABrokenManifestIsReported(t *testing.T) {
	t.Parallel()
	imp, _, logs, root := importerFixture(t)
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("name: : :"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := imp.importAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "could not import") {
		t.Errorf("a broken manifest was silent:\n%s", logs.String())
	}
}
