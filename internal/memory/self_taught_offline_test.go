package memory

import (
	"testing"

	"google.golang.org/protobuf/proto"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The offline face exists because `lobslaw learned` runs with the node
// stopped, where there is no consensus to reach. Separate from the
// online store rather than a mode on it: a store that sometimes
// bypasses raft is one misconfiguration away from doing so while the
// cluster is running.

func offlineWith(t *testing.T, recs ...*lobslawv1.SelfTaughtRecord) (*OfflineSelfTaught, *Store) {
	t.Helper()
	s, _ := newTestStore(t)
	for _, r := range recs {
		raw, err := proto.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Put(BucketSelfTaught, r.Id, raw); err != nil {
			t.Fatal(err)
		}
	}
	return NewOfflineSelfTaught(s), s
}

func offlineRecord(id string) *lobslawv1.SelfTaughtRecord {
	return &lobslawv1.SelfTaughtRecord{
		Id:    id,
		Name:  id,
		Kind:  lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL,
		State: lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE,
		Owner: "user:alice",
	}
}

func TestOfflineArchiveMovesRatherThanDeletes(t *testing.T) {
	t.Parallel()
	o, store := offlineWith(t, offlineRecord("skill:thing"))

	rec, archived, err := o.Find("skill:thing")
	if err != nil {
		t.Fatal(err)
	}
	if archived {
		t.Fatal("setup: the record should start live")
	}
	if err := o.Archive(rec, "operator asked"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(BucketSelfTaught, "skill:thing"); err == nil {
		t.Error("the artefact is still in the live bucket")
	}
	if _, err := store.Get(BucketSelfTaughtArchive, "skill:thing"); err != nil {
		t.Fatal("the artefact was deleted rather than archived")
	}

	got, wasArchived, err := o.Find("skill:thing")
	if err != nil {
		t.Fatal(err)
	}
	if !wasArchived {
		t.Error("Find does not report the artefact as archived")
	}
	if got.ArchivedReason != "operator asked" {
		t.Errorf("reason = %q", got.ArchivedReason)
	}
}

func TestOfflineArchiveRefusesPinned(t *testing.T) {
	t.Parallel()
	rec := offlineRecord("skill:keeper")
	rec.Pinned = true
	o, store := offlineWith(t, rec)

	found, _, err := o.Find("skill:keeper")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Archive(found, "housekeeping"); err == nil {
		t.Error("a pinned artefact was archived offline")
	}
	if _, err := store.Get(BucketSelfTaught, "skill:keeper"); err != nil {
		t.Error("the pinned artefact is gone")
	}
}

// Restoring returns something as PROPOSED, matching the online store:
// archiving implied a decision, and putting it straight back in force
// skips it.
func TestOfflineRestoreComesBackProposed(t *testing.T) {
	t.Parallel()
	o, store := offlineWith(t, offlineRecord("skill:thing"))
	rec, _, _ := o.Find("skill:thing")
	if err := o.Archive(rec, "unused"); err != nil {
		t.Fatal(err)
	}

	archived, _, err := o.Find("skill:thing")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Restore(archived); err != nil {
		t.Fatal(err)
	}

	raw, err := store.Get(BucketSelfTaught, "skill:thing")
	if err != nil {
		t.Fatal("restore did not put it back in the live bucket")
	}
	var back lobslawv1.SelfTaughtRecord
	if err := proto.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		t.Errorf("restored state = %v, want proposed", back.State)
	}
	if back.ArchivedReason != "" {
		t.Errorf("the archive reason survived the restore: %q", back.ArchivedReason)
	}
	if _, err := store.Get(BucketSelfTaughtArchive, "skill:thing"); err == nil {
		t.Error("the archived copy was left behind")
	}
}

// A partially-archived record — live bucket still holding a copy
// marked ARCHIVED, which a crash between the two writes would leave —
// must not appear in the live listing.
func TestPartialArchiveIsInvisibleToTheLiveList(t *testing.T) {
	t.Parallel()
	stale := offlineRecord("skill:half")
	stale.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED
	o, _ := offlineWith(t, offlineRecord("skill:real"), stale)

	live, err := o.List(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Id != "skill:real" {
		t.Errorf("live = %v; a half-archived record is still listed", artefactIDs(live))
	}
}

func artefactIDs(recs []*lobslawv1.SelfTaughtRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Id)
	}
	return out
}
