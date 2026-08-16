package memory

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// A library that only grows is a library nobody can navigate, and
// every ACTIVE skill costs tokens on every turn whether it is read or
// not. But the answer is never deletion: an agent that can silently
// erase evidence of what it taught itself is the wrong default for a
// product whose pitch is trust.

// fastCurator makes the day-scale thresholds testable without a clock
// abstraction: the same arithmetic, in a unit where a test can move.
func fastCurator() CuratorConfig {
	return CuratorConfig{StaleAfterDays: 30, ArchiveAfterDays: 90}
}

// idleFor backdates every clock the curator reads, which is what
// "unused for N days" actually means.
func idleFor(t *testing.T, s *SelfTaughtStore, id string, d time.Duration) {
	t.Helper()
	rec, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	past := timestamppb.New(time.Now().Add(-d))
	rec.CreatedAt, rec.UpdatedAt, rec.ApprovedAt = past, past, past
	if err := s.put(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := s.applyEntry(&lobslawv1.LogEntry{
		Op: lobslawv1.LogOp_LOG_OP_PUT,
		Id: id,
		Payload: &lobslawv1.LogEntry_SelfTaughtUsage{SelfTaughtUsage: &lobslawv1.SelfTaughtUsage{
			Id: id, Invocations: 1, LastUsedAt: past, FirstUsedAt: past,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func stateOf(t *testing.T, s *SelfTaughtStore, id string) lobslawv1.SelfTaughtState {
	t.Helper()
	rec, err := s.Get(id)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return rec.State
}

func TestAnUnusedArtefactGoesStaleThenArchives(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}

	idleFor(t, s, "skill:tidy", 40*24*time.Hour)
	res, err := s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staled) != 1 || res.Staled[0] != "skill:tidy" {
		t.Fatalf("staled = %v", res.Staled)
	}
	if got := stateOf(t, s, "skill:tidy"); got != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE {
		t.Fatalf("state = %v, want STALE", got)
	}

	idleFor(t, s, "skill:tidy", 100*24*time.Hour)
	res, err = s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Archived) != 1 {
		t.Fatalf("archived = %v", res.Archived)
	}
	if _, err := s.Get("skill:tidy"); err == nil {
		t.Error("the archived artefact is still in the live set")
	}

	// And it stays recoverable. Archiving is a lifecycle transition,
	// not a deletion.
	restored, err := s.Restore(ctx, "skill:tidy")
	if err != nil {
		t.Fatalf("the archived artefact could not be restored: %v", err)
	}
	if restored.Body != "body" {
		t.Errorf("body = %q", restored.Body)
	}
}

// The most important property in the file. STALE has to keep loading:
// an artefact that went out of service the moment it was marked could
// never be used again, so the transition to ARCHIVED would be a
// ratchet with no possible reprieve.
func TestAStaleArtefactStillLoads(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s, "skill:tidy", 40*24*time.Hour)
	if _, err := s.Curate(ctx, fastCurator()); err != nil {
		t.Fatal(err)
	}

	active, err := s.Active(lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("a stale artefact stopped loading: %+v", active)
	}
}

// And using it inside the window brings it back, or STALE would be
// permanent for anything seasonal — a skill for the quarterly report
// is idle for eleven weeks by nature.
func TestUsingAStaleArtefactRevivesIt(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s, "skill:tidy", 40*24*time.Hour)
	if _, err := s.Curate(ctx, fastCurator()); err != nil {
		t.Fatal(err)
	}

	s.RecordUse("skill:tidy")
	if err := s.FlushUsage(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Revived) != 1 {
		t.Fatalf("revived = %v", res.Revived)
	}
	if got := stateOf(t, s, "skill:tidy"); got != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE {
		t.Errorf("state = %v, want ACTIVE", got)
	}
}

// Marking something STALE must not reset the clock that decides
// whether it archives — otherwise the second threshold is unreachable
// and nothing ever leaves the live set.
func TestMarkingStaleDoesNotResetTheArchiveClock(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	// Already past BOTH thresholds, so one pass should archive it
	// outright — and a second pass on the same record must not find it
	// looking freshly touched.
	idleFor(t, s, "skill:tidy", 100*24*time.Hour)
	if _, err := s.Curate(ctx, fastCurator()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("skill:tidy"); err == nil {
		t.Fatal("it should have archived in one pass")
	}

	// The subtler version: stale first, then wait. The stale mark must
	// not have moved UpdatedAt forward.
	s2 := selfTaught(t, SelfLearningAuto)
	if _, err := s2.Propose(ctx, named("other", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s2, "skill:other", 100*24*time.Hour)
	before, err := s2.Get("skill:other")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.setState(ctx, before, lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE); err != nil {
		t.Fatal(err)
	}
	after, err := s2.Get("skill:other")
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.AsTime().Equal(before.UpdatedAt.AsTime()) {
		t.Fatalf("the stale mark moved UpdatedAt from %v to %v — the archive clock resets and "+
			"nothing ever leaves the live set",
			before.UpdatedAt.AsTime(), after.UpdatedAt.AsTime())
	}
	res, err := s2.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Archived) != 1 {
		t.Errorf("archived = %v; a stale artefact past the archive threshold stayed", res.Archived)
	}
}

// --- exemptions ----------------------------------------------------

// Somebody who has decided an artefact is worth keeping should not
// have to defend it from the curator every fortnight.
func TestAPinnedArtefactNeverTransitions(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	rec := named("tidy", "d", "body")
	rec.Pinned = true
	if _, err := s.Propose(ctx, rec, ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s, "skill:tidy", 1000*24*time.Hour)

	res, err := s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staled)+len(res.Archived) != 0 {
		t.Fatalf("a pinned artefact transitioned: %+v", res)
	}
	if got := stateOf(t, s, "skill:tidy"); got != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE {
		t.Errorf("state = %v", got)
	}
}

// Archiving a proposal nobody has looked at converts "not reviewed
// yet" into "declined". The pending queue is the operator's inbox, not
// the curator's to empty.
func TestAProposalIsNeverCurated(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s, "skill:tidy", 1000*24*time.Hour)

	res, err := s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staled)+len(res.Archived) != 0 {
		t.Fatalf("a proposal was curated: %+v", res)
	}
	if got := stateOf(t, s, "skill:tidy"); got != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		t.Errorf("state = %v, want PROPOSED", got)
	}
}

// A skill approved yesterday is one day old for this purpose, however
// long it sat in the proposal queue — the clock on "has anybody used
// this" cannot start before it was possible to.
func TestApprovalStartsTheClockNotCreation(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningPropose)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Get("skill:tidy")
	if err != nil {
		t.Fatal(err)
	}
	old := timestamppb.New(time.Now().Add(-200 * 24 * time.Hour))
	rec.CreatedAt, rec.UpdatedAt = old, old
	if err := s.put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, "skill:tidy", "alice"); err != nil {
		t.Fatal(err)
	}

	res, err := s.Curate(ctx, fastCurator())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Staled)+len(res.Archived) != 0 {
		t.Errorf("a just-approved artefact was curated on its creation date: %+v", res)
	}
}

// --- configuration -------------------------------------------------

// An archive threshold below the stale one is not a configuration
// anybody means. Clamped rather than refused — a node that will not
// boot because two numbers are the wrong way round is worse than one
// that curates slightly later than asked.
func TestAnInvertedThresholdPairIsClamped(t *testing.T) {
	t.Parallel()
	cfg := CuratorConfig{StaleAfterDays: 60, ArchiveAfterDays: 10}
	if cfg.archiveAfter() < cfg.staleAfter() {
		t.Errorf("archiveAfter = %v, staleAfter = %v", cfg.archiveAfter(), cfg.staleAfter())
	}
}

func TestZeroThresholdsTakeTheDefaults(t *testing.T) {
	t.Parallel()
	var cfg CuratorConfig
	if got, want := cfg.staleAfter(), time.Duration(DefaultStaleAfterDays)*24*time.Hour; got != want {
		t.Errorf("staleAfter = %v, want %v", got, want)
	}
	if got, want := cfg.archiveAfter(), time.Duration(DefaultArchiveAfterDays)*24*time.Hour; got != want {
		t.Errorf("archiveAfter = %v, want %v", got, want)
	}
	if got := cfg.interval(); got != DefaultCurateInterval {
		t.Errorf("interval = %v", got)
	}
}

// --- blast radius --------------------------------------------------

// Asserted, not assumed: a pass over a store holding other things must
// leave every one of them alone.
func TestCurationTouchesNothingOutsideTheSelfTaughtStore(t *testing.T) {
	t.Parallel()
	s := selfTaught(t, SelfLearningAuto)
	ctx := context.Background()
	if _, err := s.Propose(ctx, named("tidy", "d", "body"), ProposeIntent{}); err != nil {
		t.Fatal(err)
	}
	idleFor(t, s, "skill:tidy", 1000*24*time.Hour)

	// Records in neighbouring buckets that the curator has no way to
	// name. If curation ever grew a broader scan, this is what would
	// catch it.
	neighbours := map[string]string{
		BucketUserPrefs:     "alice",
		BucketSoulTune:      "tune",
		BucketVectorRecords: "vec-1",
	}
	for bucket, key := range neighbours {
		if err := s.store.Put(bucket, key, []byte("not the curator's")); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Curate(ctx, fastCurator()); err != nil {
		t.Fatal(err)
	}
	for bucket, key := range neighbours {
		got, err := s.store.Get(bucket, key)
		if err != nil || string(got) != "not the curator's" {
			t.Errorf("curation reached %s/%s: %q, %v", bucket, key, got, err)
		}
	}
	// And the archive it DID write is the only place the artefact
	// moved to.
	if _, err := s.store.Get(BucketSelfTaughtArchive, "skill:tidy"); err != nil {
		t.Errorf("the artefact did not reach the archive: %v", err)
	}
}
