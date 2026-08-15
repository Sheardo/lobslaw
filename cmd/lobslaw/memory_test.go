package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// testStore is a real encrypted state.db in a temp dir. The subcommands
// under test open the file themselves, so the fixture never holds it
// open across a call — same constraint the operator has.
type testStore struct {
	path string
	key  crypto.Key
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	dir := t.TempDir()

	var key crypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Pin every ambient input the offline opener consults so the
	// developer's own ~/.config/lobslaw cannot leak into a test run.
	t.Setenv("LOBSLAW_ENV", filepath.Join(dir, "no-such.env"))
	t.Setenv("LOBSLAW_CONFIG", "")
	t.Setenv("LOBSLAW_STATE_DB", "")
	t.Setenv("LOBSLAW_MEMORY_KEY", "")
	t.Setenv("TEST_MEMORY_KEY", base64.StdEncoding.EncodeToString(key[:]))

	ts := &testStore{path: filepath.Join(dir, "state.db"), key: key}
	ts.with(t, func(*memory.Store) {}) // create the file + buckets
	return ts
}

// flags returns the store-locating flags plus whatever the subcommand
// needs on top.
func (ts *testStore) flags(extra ...string) []string {
	return append([]string{"--state-db", ts.path, "--memory-key-ref", "env:TEST_MEMORY_KEY"}, extra...)
}

// with opens the store, runs fn, and closes it again. Closing matters:
// bbolt's exclusive lock means a fixture that stayed open would make
// the subcommand under test fail the way a running node does.
func (ts *testStore) with(t *testing.T, fn func(*memory.Store)) {
	t.Helper()
	store, err := memory.OpenStore(ts.path, ts.key)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close test store: %v", err)
		}
	}()
	fn(store)
}

func putVector(t *testing.T, store *memory.Store, rec *lobslawv1.VectorRecord) {
	t.Helper()
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal vector: %v", err)
	}
	if err := store.Put(memory.BucketVectorRecords, rec.Id, raw); err != nil {
		t.Fatalf("put vector: %v", err)
	}
}

func putEpisodic(t *testing.T, store *memory.Store, rec *lobslawv1.EpisodicRecord) {
	t.Helper()
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal episodic: %v", err)
	}
	if err := store.Put(memory.BucketEpisodicRecords, rec.Id, raw); err != nil {
		t.Fatalf("put episodic: %v", err)
	}
}

func mustLoad(t *testing.T, store *memory.Store, id string) *memRecord {
	t.Helper()
	rec, err := loadMemRecord(store, id)
	if err != nil {
		t.Fatalf("load %q: %v", id, err)
	}
	return rec
}

// seedCascade writes a source record, an unrelated record, and a
// consolidation built from the source.
func seedCascade(t *testing.T, ts *testStore) {
	t.Helper()
	ts.with(t, func(store *memory.Store) {
		putEpisodic(t, store, &lobslawv1.EpisodicRecord{
			Id: "src-1", Event: "deployed the gateway", Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		})
		putEpisodic(t, store, &lobslawv1.EpisodicRecord{
			Id: "src-2", Event: "unrelated", Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		})
		putVector(t, store, &lobslawv1.VectorRecord{
			Id: "cons-1", Text: "alice deployed things this week",
			SourceIds: []string{"src-1", "src-9"}, Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		})
	})
}

// TestMemoryForgetSweepsConsolidation is the property forget exists
// for: deleting a source must take the summary built from it, because
// the summary's own text and embedding still carry the content.
func TestMemoryForgetSweepsConsolidation(t *testing.T) {
	ts := newTestStore(t)
	seedCascade(t, ts)

	if err := memoryForget(ts.flags("--id", "src-1", "--apply")); err != nil {
		t.Fatalf("memory forget: %v", err)
	}

	ts.with(t, func(store *memory.Store) {
		if rec := mustLoad(t, store, "src-1"); rec != nil {
			t.Error("src-1 survived the forget")
		}
		if rec := mustLoad(t, store, "cons-1"); rec != nil {
			t.Error("cons-1 survived — a consolidation whose source was forgotten must be swept")
		}
		if rec := mustLoad(t, store, "src-2"); rec == nil {
			t.Error("src-2 was deleted; only records reachable from the matched set should go")
		}
	})
}

// TestMemoryForgetDryRunWritesNothing reopens the store rather than
// trusting the command's own report of what it would have done.
func TestMemoryForgetDryRunWritesNothing(t *testing.T) {
	ts := newTestStore(t)
	seedCascade(t, ts)

	if err := memoryForget(ts.flags("--id", "src-1")); err != nil {
		t.Fatalf("memory forget: %v", err)
	}

	ts.with(t, func(store *memory.Store) {
		for _, id := range []string{"src-1", "src-2", "cons-1"} {
			if rec := mustLoad(t, store, id); rec == nil {
				t.Errorf("%s was deleted by a dry run", id)
			}
		}
	})
}

func TestMemoryForgetRefusesEmptyFilter(t *testing.T) {
	ts := newTestStore(t)
	seedCascade(t, ts)

	err := memoryForget(ts.flags("--apply"))
	if err == nil {
		t.Fatal("an unfiltered forget was accepted; it matches every record in the store")
	}
	if !strings.Contains(err.Error(), "refusing to forget everything") {
		t.Errorf("error = %v, want the refusing-to-forget-everything guard", err)
	}
}

func TestMemoryShareRefusesUnownedRecord(t *testing.T) {
	ts := newTestStore(t)
	ts.with(t, func(store *memory.Store) {
		putVector(t, store, &lobslawv1.VectorRecord{Id: "orphan", Text: "no owner"})
	})

	err := memoryShare(ts.flags("--apply", "orphan"))
	if err == nil {
		t.Fatal("share accepted an unowned record")
	}
	if !strings.Contains(err.Error(), "unowned") {
		t.Errorf("error = %v, want it to name the unowned record as the reason", err)
	}

	ts.with(t, func(store *memory.Store) {
		rec := mustLoad(t, store, "orphan")
		if rec == nil {
			t.Fatal("orphan disappeared")
		}
		if got := rec.visibility(); got != lobslawv1.Visibility_VISIBILITY_UNSPECIFIED {
			t.Errorf("visibility = %s, want it untouched by the refused share", visLabel(got))
		}
	})
}

// TestMemoryShareRefusesWholeBatch checks the all-or-nothing rule: one
// unowned id in the batch must stop the owned ones going out too.
func TestMemoryShareRefusesWholeBatch(t *testing.T) {
	ts := newTestStore(t)
	ts.with(t, func(store *memory.Store) {
		putVector(t, store, &lobslawv1.VectorRecord{
			Id: "owned", Text: "fine", Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		})
		putVector(t, store, &lobslawv1.VectorRecord{Id: "orphan", Text: "no owner"})
	})

	if err := memoryShare(ts.flags("--apply", "owned", "orphan")); err == nil {
		t.Fatal("batch containing an unowned record was accepted")
	}

	ts.with(t, func(store *memory.Store) {
		rec := mustLoad(t, store, "owned")
		if got := rec.visibility(); got != lobslawv1.Visibility_VISIBILITY_PRIVATE {
			t.Errorf("owned record visibility = %s, want PRIVATE — the batch should not have partially applied", visLabel(got))
		}
	})
}

func TestMemoryUnshareReturnsRecordToPrivate(t *testing.T) {
	ts := newTestStore(t)
	ts.with(t, func(store *memory.Store) {
		putEpisodic(t, store, &lobslawv1.EpisodicRecord{
			Id: "e1", Event: "team retro notes", Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_SHARED,
		})
	})

	if err := memoryUnshare(ts.flags("--apply", "e1")); err != nil {
		t.Fatalf("memory unshare: %v", err)
	}

	ts.with(t, func(store *memory.Store) {
		rec := mustLoad(t, store, "e1")
		if rec == nil {
			t.Fatal("e1 disappeared")
		}
		if got := rec.visibility(); got != lobslawv1.Visibility_VISIBILITY_PRIVATE {
			t.Errorf("visibility = %s, want PRIVATE", visLabel(got))
		}
		if rec.owner() != "user:alice" {
			t.Errorf("owner = %q, want it preserved", rec.owner())
		}
	})
}

func TestMemoryShareDryRunWritesNothing(t *testing.T) {
	ts := newTestStore(t)
	ts.with(t, func(store *memory.Store) {
		putEpisodic(t, store, &lobslawv1.EpisodicRecord{
			Id: "e1", Event: "notes", Owner: "user:alice",
			Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		})
	})

	if err := memoryShare(ts.flags("e1")); err != nil {
		t.Fatalf("memory share: %v", err)
	}

	ts.with(t, func(store *memory.Store) {
		if got := mustLoad(t, store, "e1").visibility(); got != lobslawv1.Visibility_VISIBILITY_PRIVATE {
			t.Errorf("visibility = %s, want PRIVATE — a dry run must not write", visLabel(got))
		}
	})
}

// TestOfflineStoreLockedDatabase is the whole reason translateOpenError
// exists. It costs the bbolt open timeout (5s) because the lock wait is
// the thing being exercised — a faked error would not prove the real
// open path produces one that matches.
func TestOfflineStoreLockedDatabase(t *testing.T) {
	ts := newTestStore(t)

	held, err := memory.OpenStore(ts.path, ts.key)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer func() { _ = held.Close() }()

	err = memoryList(ts.flags())
	if err == nil {
		t.Fatal("memory list succeeded against a locked state.db")
	}
	msg := err.Error()
	if !strings.Contains(msg, "the node is running") {
		t.Errorf("error = %q, want it to say the node is running", msg)
	}
	if strings.TrimSpace(msg) == "timeout" || strings.HasSuffix(msg, ": timeout") {
		t.Errorf("error = %q, want the bare bbolt timeout translated", msg)
	}
}

func TestTranslateOpenErrorPassesThroughOtherErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("permission denied")
	if got := translateOpenError("/tmp/state.db", sentinel); !errors.Is(got, sentinel) {
		t.Errorf("translateOpenError rewrote an unrelated error: %v", got)
	}
}

// TestOfflineStoreMissingFileIsAnError guards against memory.OpenStore's
// create-on-open turning a mistyped path into an empty database that
// reports zero records instead of failing.
func TestOfflineStoreMissingFileIsAnError(t *testing.T) {
	ts := newTestStore(t)
	missing := filepath.Join(filepath.Dir(ts.path), "typo.db")

	err := memoryList([]string{"--state-db", missing, "--memory-key-ref", "env:TEST_MEMORY_KEY"})
	if err == nil {
		t.Fatal("memory list accepted a nonexistent state.db")
	}
	if !strings.Contains(err.Error(), "typo.db") {
		t.Errorf("error = %v, want it to name the missing path", err)
	}
}

// TestMemoryListAndShowRender walks the rendering paths — the field
// tables, the ! marker, the JSON shape — which nothing else covers
// because every other test asserts on stored state.
func TestMemoryListAndShowRender(t *testing.T) {
	ts := newTestStore(t)
	seedCascade(t, ts)
	ts.with(t, func(store *memory.Store) {
		putVector(t, store, &lobslawv1.VectorRecord{
			Id: "orphan", Text: "written before ownership existed", Scope: "episodic",
			Embedding: []float32{0.1, 0.2, 0.3},
		})
	})

	for _, args := range [][]string{
		ts.flags(),
		ts.flags("--json"),
		ts.flags("--kind", "vector"),
		ts.flags("--unowned"),
		ts.flags("--owner", "user:alice", "--limit", "1"),
		ts.flags("--tag", "nope"),
	} {
		if err := memoryList(args); err != nil {
			t.Errorf("memory list %v: %v", args, err)
		}
	}

	if err := memoryList(ts.flags("--kind", "bogus")); err == nil {
		t.Error("--kind bogus was accepted")
	}

	for _, id := range []string{"src-1", "cons-1", "orphan"} {
		if err := memoryShow(ts.flags(id)); err != nil {
			t.Errorf("memory show %s: %v", id, err)
		}
		if err := memoryShow(ts.flags("--json", id)); err != nil {
			t.Errorf("memory show --json %s: %v", id, err)
		}
	}
}

// TestMemoryShowListsConsolidations checks the referenced-by list a
// `show` prints — it is the operator's warning that a forget here will
// cascade.
func TestMemoryShowListsConsolidations(t *testing.T) {
	ts := newTestStore(t)
	seedCascade(t, ts)

	ts.with(t, func(store *memory.Store) {
		refs, err := referencedBy(store, "src-1")
		if err != nil {
			t.Fatalf("referencedBy: %v", err)
		}
		if len(refs) != 1 || refs[0] != "cons-1" {
			t.Errorf("referencedBy(src-1) = %v, want [cons-1]", refs)
		}
		if refs, err := referencedBy(store, "src-2"); err != nil || len(refs) != 0 {
			t.Errorf("referencedBy(src-2) = %v, %v; want no consolidations", refs, err)
		}
	})
}

func TestMemoryShowUnknownID(t *testing.T) {
	ts := newTestStore(t)
	err := memoryShow(ts.flags("nope"))
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("memory show of an unknown id = %v, want an error naming the id", err)
	}
}
