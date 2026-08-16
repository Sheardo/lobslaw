package memory

import (
	"context"
	"errors"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Provenance by location is the design: if a record is here, the agent
// wrote it. These are about the properties that follow from that —
// nothing is silently deleted, "off" means absent, and "forget
// everything you taught yourself" is one operation.

func selfTaught(t *testing.T, mode SelfLearningMode) *SelfTaughtStore {
	t.Helper()
	node, fsm := newTestRaft(t)
	s, err := NewSelfTaughtStore(node, fsm.Store(), mode)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("no store; this helper is for the modes that have one")
	}
	return s
}

func aSkill(name string) *lobslawv1.SelfTaughtRecord {
	return &lobslawv1.SelfTaughtRecord{
		Kind:   lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL,
		Name:   name,
		Body:   "how to do the thing",
		Origin: lobslawv1.SelfTaughtOrigin_SELF_TAUGHT_ORIGIN_REVIEW_FORK,
		Owner:  "user:alice",
	}
}

// "off" has to be verifiable by absence. A store that exists and
// refuses writes is a different, weaker claim than no store at all.
func TestOffBuildsNoStore(t *testing.T) {
	t.Parallel()
	node, fsm := newTestRaft(t)
	s, err := NewSelfTaughtStore(node, fsm.Store(), SelfLearningOff)
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Error("mode=off produced a store; the capability should be absent, not guarded")
	}
}

// A typo must never be the reason an agent started following its own
// instructions.
func TestUnknownModeIsOff(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "on", "true", "enabled", "PROPOSE ", "nonsense"} {
		got := ParseSelfLearningMode(in)
		want := SelfLearningOff
		if in == "PROPOSE " {
			want = SelfLearningPropose // trimmed and case-folded
		}
		if got != want {
			t.Errorf("ParseSelfLearningMode(%q) = %q, want %q", in, got, want)
		}
	}
	if ParseSelfLearningMode("auto") != SelfLearningAuto {
		t.Error("auto did not parse")
	}
}

// The mode decides the initial state, not the caller. A caller that
// could choose would eventually choose ACTIVE somewhere and the
// operator's setting would stop meaning what it says.
func TestModeDecidesInitialState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	proposeStore := selfTaught(t, SelfLearningPropose)
	rec, err := proposeStore.Propose(ctx, aSkill("tidy-notes"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		t.Errorf("propose mode produced state %v", rec.State)
	}

	autoStore := selfTaught(t, SelfLearningAuto)
	rec, err = autoStore.Propose(ctx, aSkill("tidy-notes"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE {
		t.Errorf("auto mode produced state %v", rec.State)
	}
}

// The caller cannot smuggle a state past the mode.
func TestCallerSuppliedStateIsIgnored(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	rec := aSkill("sneaky")
	rec.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE

	out, err := s.Propose(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		t.Errorf("a caller-supplied ACTIVE survived propose mode: %v", out.State)
	}
}

// Proposed artefacts are inert: Active must not return them.
func TestProposedArtefactsAreInert(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	ctx := context.Background()
	if _, err := s.Propose(ctx, aSkill("tidy-notes")); err != nil {
		t.Fatal(err)
	}

	active, err := s.Active(lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("a proposed artefact is loadable: %+v", active)
	}

	if _, err := s.Approve(ctx, "skill:tidy-notes", "alice"); err != nil {
		t.Fatal(err)
	}
	active, err = s.Active(lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("an approved artefact did not become active: %+v", active)
	}
	if active[0].ApprovedBy != "alice" {
		t.Errorf("approved_by = %q; the decision has no attribution", active[0].ApprovedBy)
	}
}

func TestApprovingSomethingAlreadyActiveIsRefused(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, aSkill("thing")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, "skill:thing", "alice"); !errors.Is(err, ErrNotProposed) {
		t.Errorf("err = %v, want ErrNotProposed", err)
	}
}

// Deletion is not a lifecycle transition. An agent that can silently
// erase evidence of what it taught itself is the wrong default for a
// product whose pitch is trust.
func TestArchivingIsRecoverable(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, aSkill("thing")); err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(ctx, "skill:thing", "unused for a month"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("skill:thing"); err == nil {
		t.Error("an archived artefact is still in the live set")
	}

	archived, err := s.List(SelfTaughtQuery{Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 {
		t.Fatalf("archive = %+v; the artefact was deleted rather than archived", archived)
	}
	if archived[0].ArchivedReason != "unused for a month" {
		t.Errorf("reason = %q; the archive cannot say why", archived[0].ArchivedReason)
	}

	// And it comes back PROPOSED, not ACTIVE: something archived
	// itself out of use once, and restoring it straight into force
	// skips the decision that archiving implied.
	restored, err := s.Restore(ctx, "skill:thing")
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		t.Errorf("restored state = %v, want proposed", restored.State)
	}
}

// A person who has decided something is worth keeping should not have
// to defend it from the curator every fortnight.
func TestPinnedArtefactsResistArchiving(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	rec := aSkill("keeper")
	rec.Pinned = true
	if _, err := s.Propose(ctx, rec); err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(ctx, "skill:keeper", "housekeeping"); err == nil {
		t.Error("a pinned artefact was archived")
	}
	if _, err := s.Get("skill:keeper"); err != nil {
		t.Errorf("the pinned artefact is gone: %v", err)
	}
}

// "Forget everything you taught yourself" is one operation precisely
// because provenance is a location.
func TestDiscardAllArchivesTheLot(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	for _, name := range []string{"one", "two", "three"} {
		if _, err := s.Propose(ctx, aSkill(name)); err != nil {
			t.Fatal(err)
		}
	}
	keeper := aSkill("keeper")
	keeper.Pinned = true
	if _, err := s.Propose(ctx, keeper); err != nil {
		t.Fatal(err)
	}

	n, err := s.DiscardAll(ctx, "user asked")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("discarded %d, want 3 (the pinned one stays)", n)
	}
	live, _ := s.List(SelfTaughtQuery{})
	if len(live) != 1 || live[0].Name != "keeper" {
		t.Errorf("live set = %+v; discard took the pinned artefact too", live)
	}
	archived, _ := s.List(SelfTaughtQuery{Archived: true})
	if len(archived) != 3 {
		t.Errorf("archive = %d records, want 3 — discard deleted rather than archived", len(archived))
	}
}

// Re-proposing is a new version of one artefact, not a second one.
func TestReproposalBumpsTheVersion(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()

	first, err := s.Propose(ctx, aSkill("thing"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Propose(ctx, aSkill("thing"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != first.Version+1 {
		t.Errorf("version went %d -> %d", first.Version, second.Version)
	}
	if !second.CreatedAt.AsTime().Equal(first.CreatedAt.AsTime()) {
		t.Error("created_at was reset; the artefact's age was lost")
	}
	live, _ := s.List(SelfTaughtQuery{})
	if len(live) != 1 {
		t.Errorf("re-proposal produced %d records", len(live))
	}
}

// Counter bumps are high-frequency and low-value; paying consensus for
// each one is the obvious way to make this worse than the sidecar file
// it replaces.
func TestUsageIsBatchedUntilFlush(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, aSkill("thing")); err != nil {
		t.Fatal(err)
	}

	for range 10 {
		s.RecordUse("skill:thing")
	}
	if got := s.Usage("skill:thing").Invocations; got != 0 {
		t.Errorf("invocations = %d before a flush; the bumps reached raft one at a time", got)
	}

	if err := s.FlushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	u := s.Usage("skill:thing")
	if u.Invocations != 10 {
		t.Errorf("invocations = %d, want 10", u.Invocations)
	}
	if u.FirstUsedAt == nil || u.LastUsedAt == nil {
		t.Error("no usage timestamps; staleness cannot be computed")
	}

	// A second batch accumulates rather than replacing.
	s.RecordUse("skill:thing")
	if err := s.FlushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Usage("skill:thing").Invocations; got != 11 {
		t.Errorf("invocations = %d after a second flush, want 11", got)
	}
}

func TestFlushWithNothingPendingIsFine(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	if err := s.FlushUsage(context.Background()); err != nil {
		t.Errorf("flushing an empty batch errored: %v", err)
	}
}

// A listing scoped to one person must not show another's.
func TestListScopesByOwnerToo(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	mine := aSkill("mine")
	theirs := aSkill("theirs")
	theirs.Owner = "user:bob"
	if _, err := s.Propose(ctx, mine); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, theirs); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(SelfTaughtQuery{Owner: "user:alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "mine" {
		t.Errorf("owner filter returned %+v", got)
	}
}

func TestProposeRequiresNameAndKind(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()

	if _, err := s.Propose(ctx, &lobslawv1.SelfTaughtRecord{
		Kind: lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL,
	}); err == nil {
		t.Error("an artefact with no name was accepted")
	}
	if _, err := s.Propose(ctx, &lobslawv1.SelfTaughtRecord{Name: "x"}); err == nil {
		t.Error("an artefact with no kind was accepted")
	}
}
