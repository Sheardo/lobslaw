package skills

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// The log is the source of truth; the directory on disk is a cache.
//
// Two R18 acceptance criteria were never verified: that a node with no
// storage mount and no skills directory serves the full library from
// the log, and that deleting the cache and restarting restores every
// skill BYTE-IDENTICALLY. Both are properties of the materialiser, and
// both are the kind of thing that works until the day it does not.
//
// Byte-identical is not fussiness. A detached signature is over the
// exact manifest bytes, so a materialiser that re-renders a manifest —
// normalising key order, stripping a comment, trimming trailing
// whitespace — produces a directory whose signature no longer
// verifies, and the skill fails to load on every node at once.
//
// materialise_stored_test.go already covers a signed skill still
// verifying on disk, and a hand-edited cache being corrected. What it
// does not cover is the cache being GONE — a fresh container, a wiped
// volume — which is the case these two add.

func newMat(t *testing.T, root string) *Materialiser {
	t.Helper()
	m, err := NewMaterialiser(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func storedFixture(t *testing.T) []StoredSkill {
	t.Helper()
	// Deliberately NOT already-normalised: a comment, an unusual key
	// order, trailing whitespace. A re-render tidies all three away and
	// the byte comparison is what notices.
	manifest := []byte("# published by example.com\nversion: 1.2.3\nname: tidy\n" +
		"runtime: python\nhandler: handler.py   \n")
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return []StoredSkill{{
		Name:         "tidy",
		Version:      "1.2.3",
		ManifestYAML: manifest,
		ManifestSig:  ed25519.Sign(priv, manifest),
		Files: map[string][]byte{
			"handler.py":     []byte("print('hi')\n"),
			"lib/helper.py":  []byte("# nested\n"),
			"data/fixture.b": {0x00, 0x01, 0x02, 0xff},
		},
	}}
}

// snapshot reads every file under root as bytes, keyed by relative path.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A node with no storage mount and no skills directory serves the full
// library from the log.
func TestAnEmptyNodeMaterialisesTheWholeLibrary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newMat(t, root)

	res, err := m.MaterialiseStored(storedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("refused: %v", res.Refused)
	}

	files := snapshot(t, m.ImportedRoot())
	for _, want := range []string{"handler.py", "lib/helper.py", "data/fixture.b", "manifest.yaml"} {
		found := false
		for path := range files {
			if filepath.Base(path) == filepath.Base(want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not written; the library is not complete from the log", want)
		}
	}
}

// Deleting the cache and materialising again restores every byte.
func TestDeletingTheCacheRestoresItByteIdentically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newMat(t, root)
	stored := storedFixture(t)

	if _, err := m.MaterialiseStored(stored); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, m.ImportedRoot())
	if len(before) == 0 {
		t.Fatal("nothing was written the first time")
	}

	// The cache is gone — a fresh container, a wiped volume, an
	// operator clearing a directory they thought was scratch.
	if err := os.RemoveAll(m.ImportedRoot()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MaterialiseStored(stored); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, m.ImportedRoot())

	if len(after) != len(before) {
		t.Fatalf("restored %d files, had %d", len(after), len(before))
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s was not restored", path)
			continue
		}
		if got != want {
			t.Errorf("%s differs after restore:\n got %q\nwant %q", path, got, want)
		}
	}
}
