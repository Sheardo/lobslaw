package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

func newTestBots(t *testing.T) *BotService {
	t.Helper()
	raft, fsm := newTestRaft(t)
	return NewBotService(raft, fsm.store)
}

func TestBotCreateAndGet(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	got, err := svc.Put(ctx, &lobslawv1.BotRecord{
		Id:           "engineering",
		DisplayName:  "Engineering",
		Instructions: "You are the engineer for XYZ.",
		Enabled:      true,
	}, 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got.GetRevision() != 1 {
		t.Errorf("revision = %d, want 1 on create", got.GetRevision())
	}
	if got.GetCreatedAt() == nil {
		t.Error("created_at was not stamped")
	}

	read, err := svc.Get(ctx, "engineering")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.GetInstructions() != "You are the engineer for XYZ." {
		t.Errorf("instructions = %q", read.GetInstructions())
	}
}

func TestBotGetUnknownIsNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	if _, err := svc.Get(context.Background(), "nobody"); !errors.Is(err, ErrBotNotFound) {
		t.Errorf("err = %v, want ErrBotNotFound", err)
	}
}

// A stale reader must not silently lose somebody else's edit. Same CAS
// contract as the soul overlay, and for the same reason: every write
// replaces the whole record from the writer's own read.
func TestBotStaleWriteIsRejected(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "marketing", Enabled: true}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "marketing", Instructions: "first"}, 1); err != nil {
		t.Fatalf("first update: %v", err)
	}
	_, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "marketing", Instructions: "second"}, 1)
	if !errors.Is(err, ErrClaimConflict) {
		t.Errorf("err = %v, want ErrClaimConflict for a write from a stale read", err)
	}

	read, err := svc.Get(ctx, "marketing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.GetInstructions() != "first" {
		t.Errorf("instructions = %q, want the first update to have survived", read.GetInstructions())
	}
}

func TestBotCreateWithNonZeroRevisionIsRejected(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	_, err := svc.Put(context.Background(), &lobslawv1.BotRecord{Id: "ghost"}, 7)
	if !errors.Is(err, ErrClaimConflict) {
		t.Errorf("err = %v, want ErrClaimConflict creating against a revision that never existed", err)
	}
}

// An id becomes a principal, a soul-overlay key suffix and a policy
// subject. A colon or a space in one silently changes what a rule
// matches, so the registry is the place that refuses it.
func TestBotIDIsValidated(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()
	for _, id := range []string{"", "Engineering", "eng ops", "bot:eng", "-eng", strings.Repeat("a", 64)} {
		if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: id}, 0); err == nil {
			t.Errorf("Put accepted id %q", id)
		}
	}
}

// The brief rides on every turn this bot takes, so an unbounded one is
// an unbounded per-turn cost nothing else would report.
func TestBotInstructionsAreCapped(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	_, err := svc.Put(context.Background(), &lobslawv1.BotRecord{
		Id:           "verbose",
		Instructions: strings.Repeat("x", MaxBotInstructions+1),
	}, 0)
	if err == nil {
		t.Fatal("Put accepted instructions over the cap")
	}
	if !strings.Contains(err.Error(), "every turn") {
		t.Errorf("error does not say why the cap exists: %v", err)
	}
}

func TestBotMayNotMessageItself(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	_, err := svc.Put(context.Background(), &lobslawv1.BotRecord{
		Id:         "narcissus",
		MayMessage: []string{"narcissus"},
	}, 0)
	if err == nil {
		t.Error("Put accepted a bot listing itself as a message target")
	}
}

// An update must not be able to promote a bot to chief. There is one
// chief, it owns the human-facing channels, and a second would mean
// two agents answering the same Telegram message.
func TestBotUpdateCannotClaimTheChiefFlag(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "pretender"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "pretender", IsChief: true}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.GetIsChief() {
		t.Error("an update promoted a bot to chief")
	}
}

func TestChiefCannotBeDeleted(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	if _, err := svc.EnsureChief(ctx, "Chief"); err != nil {
		t.Fatalf("EnsureChief: %v", err)
	}
	err := svc.Delete(ctx, ChiefBotID)
	if err == nil {
		t.Fatal("Delete removed the chief")
	}
	if !strings.Contains(err.Error(), "answers your messages") {
		t.Errorf("error does not say what would break: %v", err)
	}
}

func TestBotDeleteRemovesIt(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "temporary"}, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, "temporary"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, "temporary"); !errors.Is(err, ErrBotNotFound) {
		t.Errorf("err = %v, want ErrBotNotFound after delete", err)
	}
}

// The upgrade story: EnsureChief is idempotent and must never
// overwrite instructions an operator has since edited.
func TestEnsureChiefIsIdempotent(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	first, err := svc.EnsureChief(ctx, "Chief")
	if err != nil {
		t.Fatalf("EnsureChief: %v", err)
	}
	if !first.GetIsChief() {
		t.Error("seeded chief is not marked chief")
	}
	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{
		Id:           ChiefBotID,
		DisplayName:  "Chief",
		Instructions: "operator wrote this",
		Enabled:      true,
	}, first.GetRevision()); err != nil {
		t.Fatalf("operator edit: %v", err)
	}

	again, err := svc.EnsureChief(ctx, "Chief")
	if err != nil {
		t.Fatalf("second EnsureChief: %v", err)
	}
	if again.GetInstructions() != "operator wrote this" {
		t.Errorf("EnsureChief overwrote an operator's edit: %q", again.GetInstructions())
	}
}

// The chief's soul-overlay key must be the pre-existing constant, or
// an upgraded cluster wakes up with a default personality — a silent
// regression that lands on the user, not the operator.
func TestChiefKeepsThePreExistingSoulKey(t *testing.T) {
	t.Parallel()
	if got := SoulTuneRecordIDFor(ChiefBotID); got != SoulTuneRecordID {
		t.Errorf("chief overlay key = %q, want the pre-existing %q", got, SoulTuneRecordID)
	}
	if got := SoulTuneRecordIDFor(""); got != SoulTuneRecordID {
		t.Errorf("unnamed bot overlay key = %q, want the chief's %q", got, SoulTuneRecordID)
	}
	if got, want := SoulTuneRecordIDFor("engineering"), SoulTuneRecordID+":engineering"; got != want {
		t.Errorf("bot overlay key = %q, want %q", got, want)
	}
}

func TestBotListPutsChiefFirst(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	for _, id := range []string{"zulu", "alpha"} {
		if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: id}, 0); err != nil {
			t.Fatalf("create %q: %v", id, err)
		}
	}
	if _, err := svc.EnsureChief(ctx, "Chief"); err != nil {
		t.Fatalf("EnsureChief: %v", err)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(list))
	for i, b := range list {
		got[i] = b.GetId()
	}
	want := []string{ChiefBotID, "alpha", "zulu"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List order = %v, want %v", got, want)
	}
}

// A bucket missing from archiveKinds is silently absent from every
// backup and portable export — the allowlist is opt-in by design, so
// forgetting one loses the team on a restore.
func TestBotsAreExportable(t *testing.T) {
	t.Parallel()
	for _, k := range archiveKinds {
		if k.bucket == BucketBots {
			return
		}
	}
	t.Errorf("%q is not in archiveKinds; bots would not survive a backup/restore", BucketBots)
}

// The cycle check happens on write, so a loop is one error message
// about a decision somebody just made rather than a depth counter
// enforced on every message forever.
func TestBotUpdateRefusesAnEdgeThatClosesALoop(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	ctx := context.Background()

	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{Id: "engineering"}, 0); err != nil {
		t.Fatalf("create engineering: %v", err)
	}
	if _, err := svc.Put(ctx, &lobslawv1.BotRecord{
		Id: "marketing", MayMessage: []string{"engineering"},
	}, 0); err != nil {
		t.Fatalf("create marketing: %v", err)
	}

	_, err := svc.Put(ctx, &lobslawv1.BotRecord{
		Id: "engineering", MayMessage: []string{"marketing"},
	}, 1)
	if err == nil {
		t.Fatal("an edge closing a marketing↔engineering loop was accepted")
	}
	for _, want := range []string{"marketing", "engineering", "loop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}

	// And nothing was written — a refused edit must not half-apply.
	read, gerr := svc.Get(ctx, "engineering")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if len(read.GetMayMessage()) != 0 {
		t.Errorf("the refused edge was written anyway: %v", read.GetMayMessage())
	}
}

// An edge to a bot that does not exist yet is fine: refusing it would
// make the order two bots are created in matter.
func TestBotEdgeToAnUnknownBotIsAccepted(t *testing.T) {
	t.Parallel()
	svc := newTestBots(t)
	if _, err := svc.Put(context.Background(), &lobslawv1.BotRecord{
		Id: "chief", MayMessage: []string{"not-created-yet"},
	}, 0); err != nil {
		t.Errorf("an edge to a future bot was refused: %v", err)
	}
}
