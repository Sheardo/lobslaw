package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// History exists so a refinement that turned out worse can be undone
// without the original being rewritten from memory. Bounded, because
// every version lives in every snapshot on every node — unbounded
// history is a store-growth problem that surfaces months later as slow
// snapshots.

func TestApprovingARefinementKeepsTheOldVersion(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	ctx := context.Background()

	if _, err := s.Propose(ctx, named("tidy", "d1", "v1 body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, "skill:tidy", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, named("tidy", "d2", "v2 body"), ProposeIntent{
		Refines: "skill:tidy", Rationale: "better",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePending(ctx, "skill:tidy", "alice"); err != nil {
		t.Fatal(err)
	}

	history, err := s.History("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d versions, want the superseded one: %+v", len(history), history)
	}
	if history[0].Body != "v1 body" {
		t.Errorf("history[0].Body = %q, want the version that was replaced", history[0].Body)
	}
	// A pending revision is a proposal against a version, not part of
	// it — keeping it would make a rollback restore a suggestion
	// somebody had already declined.
	if history[0].Pending != nil {
		t.Error("the snapshot carried a pending revision")
	}
}

func TestRollbackRestoresAPriorBody(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()

	if _, err := s.Propose(ctx, named("tidy", "d1", "v1 body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, named("tidy", "d2", "v2 body"), ProposeIntent{
		Refines: "skill:tidy", Rationale: "supposedly better",
	}); err != nil {
		t.Fatal(err)
	}

	back, err := s.Rollback(ctx, "skill:tidy", 1)
	if err != nil {
		t.Fatal(err)
	}
	if back.Body != "v1 body" {
		t.Errorf("body = %q, want the rolled-back one", back.Body)
	}
	// A NEW version number, not the old one. Reusing it would put two
	// different records at one version and the history would stop
	// being a sequence anybody can reason about.
	if back.Version != 3 {
		t.Errorf("version = %d, want 3 — a rollback is a new version", back.Version)
	}

	// And the rollback is itself undoable: an operator who rolled back
	// to the wrong version must not have destroyed the one they were
	// on.
	history, err := s.History("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range history {
		if h.Body == "v2 body" {
			found = true
		}
	}
	if !found {
		t.Errorf("the version rolled away from is gone: %+v", history)
	}
}

func TestRollbackToAMissingVersionFails(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "v1"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback(ctx, "skill:tidy", 99); err == nil {
		t.Error("rolling back to a version that never existed succeeded")
	}
}

// The depth bounds PRIOR versions; the active one is always kept and
// does not count. That is the first question anybody asks about the
// setting, so it is worth pinning.
func TestHistoryIsBoundedByDepth(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	s.SetLimits(0, 0, 3)
	ctx := context.Background()

	if _, err := s.Propose(ctx, named("tidy", "d", "v1"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 8; i++ {
		if _, err := s.Propose(ctx, named("tidy", "d", fmt.Sprintf("v%d", i)), ProposeIntent{
			Refines: "skill:tidy", Rationale: "iterating",
		}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := s.History("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d versions, want 3", len(history))
	}
	// The newest prior versions are the ones kept — rolling back to
	// last week is the case that matters, not to the first draft.
	if history[0].Version != 7 || history[2].Version != 5 {
		t.Errorf("kept versions %d..%d; the oldest should have been pruned",
			history[2].Version, history[0].Version)
	}

	// And the active version is untouched by the pruning.
	current, err := s.Get("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	if current.Body != "v8" {
		t.Errorf("active body = %q", current.Body)
	}
}

// Keys are zero-padded so a prefix scan is version order. Without it
// v10 sorts between v1 and v2 and "the oldest version" is whichever
// looks smallest as a string.
func TestHistoryOrdersPastTenVersions(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	s.SetLimits(0, 0, 20)
	ctx := context.Background()

	if _, err := s.Propose(ctx, named("tidy", "d", "v1"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 12; i++ {
		if _, err := s.Propose(ctx, named("tidy", "d", fmt.Sprintf("v%d", i)), ProposeIntent{
			Refines: "skill:tidy", Rationale: "iterating",
		}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := s.History("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 11 {
		t.Fatalf("history = %d versions", len(history))
	}
	for i := 1; i < len(history); i++ {
		if history[i-1].Version <= history[i].Version {
			t.Fatalf("history is not newest-first at %d: %d then %d",
				i, history[i-1].Version, history[i].Version)
		}
	}
	if history[0].Version != 11 {
		t.Errorf("newest prior version = %d, want 11", history[0].Version)
	}
}

func TestHistoryKeyRoundTrips(t *testing.T) {
	t.Parallel()
	key := historyKey("skill:tidy", 7)
	id, version, ok := parseHistoryKey(key)
	if !ok || id != "skill:tidy" || version != 7 {
		t.Errorf("parseHistoryKey(%q) = %q, %d, %v", key, id, version, ok)
	}
	if _, _, ok := parseHistoryKey("no-at-sign"); ok {
		t.Error("a key with no version parsed")
	}
}

// --- size limits ---------------------------------------------------

// Every raft apply replicates to every node and lives in snapshots
// thereafter, so one oversized artefact bloats every node permanently.
func TestOversizedArtefactIsRefused(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	s.SetLimits(100, 300, 0)
	ctx := context.Background()

	big := named("huge", "d", strings.Repeat("x", 200))
	_, err := s.Propose(ctx, big, ProposeIntent{})
	if !errors.Is(err, ErrArtefactTooLarge) {
		t.Fatalf("err = %v, want ErrArtefactTooLarge", err)
	}
	if live, _ := s.List(SelfTaughtQuery{}); len(live) != 0 {
		t.Errorf("an oversized artefact was stored: %+v", live)
	}
}

// The error names the file, because "too large" without saying which
// leaves an author guessing at a bundle they may not have assembled by
// hand.
func TestOversizedFileIsNamed(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	s.SetLimits(100, 10000, 0)

	rec := named("bundle", "d", "small body")
	rec.Files = map[string]string{
		"references/small.md": "fine",
		"references/huge.md":  strings.Repeat("x", 200),
	}
	_, err := s.Propose(context.Background(), rec, ProposeIntent{})
	if !errors.Is(err, ErrArtefactTooLarge) {
		t.Fatalf("err = %v, want ErrArtefactTooLarge", err)
	}
	if !strings.Contains(err.Error(), "references/huge.md") {
		t.Errorf("err = %q; it does not name the offending file", err)
	}
}

// Several small files can still exceed the total, and that has to be
// caught separately or a bundle of a hundred just-under-limit files
// passes.
func TestTotalSizeIsCheckedSeparately(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	s.SetLimits(100, 250, 0)

	rec := named("bundle", "d", "body")
	rec.Files = map[string]string{
		"a.md": strings.Repeat("x", 90),
		"b.md": strings.Repeat("x", 90),
		"c.md": strings.Repeat("x", 90),
	}
	_, err := s.Propose(context.Background(), rec, ProposeIntent{})
	if !errors.Is(err, ErrArtefactTooLarge) {
		t.Errorf("err = %v; three files under the per-file limit exceeded the total", err)
	}
}

// A refinement must be checked too, or the limit is only enforced on
// the first version.
func TestRefinementIsSizeChecked(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	s.SetLimits(100, 10000, 0)
	ctx := context.Background()

	if _, err := s.Propose(ctx, named("tidy", "d", "small"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Propose(ctx, named("tidy", "d", strings.Repeat("x", 200)), ProposeIntent{
		Refines: "skill:tidy", Rationale: "much bigger",
	})
	if !errors.Is(err, ErrArtefactTooLarge) {
		t.Errorf("err = %v; the limit is only enforced on a first version", err)
	}
	// And the live version is untouched by the refused refinement.
	current, _ := s.Get("skill:tidy")
	if current.Body != "small" || current.Pending != nil {
		t.Errorf("a refused refinement altered the artefact: %+v", current)
	}
}

func TestSizeLimitsHaveWorkableDefaults(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	// A realistic skill: a few kilobytes of prose and a reference.
	rec := named("realistic", "does a thing", strings.Repeat("x", 4000))
	rec.Files = map[string]string{"references/api.md": strings.Repeat("y", 20000)}
	if _, err := s.Propose(context.Background(), rec, ProposeIntent{}); err != nil {
		t.Errorf("an ordinary skill was refused by the default limits: %v", err)
	}
}

var _ = lobslawv1.SelfTaughtRecord{}
