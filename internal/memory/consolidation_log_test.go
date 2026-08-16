package memory

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Memory that silently rewrites itself and cannot be inspected is a
// trust problem. These are about the record being complete enough to
// answer the two questions a user actually asks: "why did it merge
// those" and "why did it NOT merge those".

// scriptedAdjudicator returns a fixed verdict, so a test can drive
// each branch without an LLM.
type scriptedAdjudicator struct {
	verdict MergeVerdict
	reason  string
	merged  string
	err     error
}

func (s scriptedAdjudicator) AdjudicateMerge(context.Context, *lobslawv1.Cluster) (MergeDecision, error) {
	if s.err != nil {
		return MergeDecision{}, s.err
	}
	return MergeDecision{Verdict: s.verdict, Reason: s.reason, MergedText: s.merged}, nil
}

// A verdict that changes nothing is still recorded. A log of changes
// alone cannot answer "why did it leave those two separate".
func TestKeepDistinctIsRecorded(t *testing.T) {
	t.Parallel()
	d, store := dreamWithClusterOf(t, scriptedAdjudicator{
		verdict: MergeVerdictKeepDistinct,
		reason:  "one is about the cat, the other about the car",
	})

	if _, err := d.mergePhase(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := ListConsolidations(store, ConsolidationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1 — a no-op verdict was not recorded", len(entries))
	}
	e := entries[0]
	if e.Verdict != "keep_distinct" {
		t.Errorf("verdict = %q", e.Verdict)
	}
	if e.Reason != "one is about the cat, the other about the car" {
		t.Errorf("reason lost: %q", e.Reason)
	}
	if e.ResultId != "" {
		t.Errorf("result_id = %q; nothing was produced", e.ResultId)
	}
	if !e.Applied {
		t.Error("a no-op verdict is marked not-applied")
	}
	if e.MemberCount != 2 || e.Owner != "user:alice" {
		t.Errorf("members=%d owner=%q, want 2 and user:alice", e.MemberCount, e.Owner)
	}
}

// The source ids have to survive the merge that removed the originals,
// or the log cannot say what went into a consolidation.
func TestMergeRecordsWhatWentIntoIt(t *testing.T) {
	t.Parallel()
	d, store := dreamWithClusterOf(t, scriptedAdjudicator{
		verdict: MergeVerdictMerge,
		reason:  "same fact stated twice",
		merged:  "alice likes tea",
	})

	if _, err := d.mergePhase(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := ListConsolidations(store, ConsolidationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Verdict != "merge" || !e.Applied {
		t.Errorf("verdict=%q applied=%v", e.Verdict, e.Applied)
	}
	if len(e.SourceIds) != 2 {
		t.Fatalf("source_ids = %v, want both originals", e.SourceIds)
	}
	if e.ResultId == "" {
		t.Error("no result_id; the log cannot point at what replaced them")
	}

	// The originals are gone from the store, and the log is the only
	// remaining record of them.
	for _, id := range e.SourceIds {
		if _, err := store.Get(BucketVectorRecords, id); err == nil {
			t.Errorf("source %s survived the merge", id)
		}
	}
}

// A decision that was made and then failed to apply is exactly when a
// user notices something is off. Recording it as "nothing happened"
// would hide the case the log exists for.
//
// Exercised directly rather than through mergePhase: the only way
// applyMerge fails on a well-formed cluster is a cross-owner one, and
// findClusters refuses to union across owners in the first place. The
// guard there is defence-in-depth for a case the clustering already
// prevents, so driving it end-to-end would mean breaking clustering to
// test logging.
func TestFailedApplyIsRecordedAsAttempted(t *testing.T) {
	t.Parallel()
	d, store := dreamWithClusterOf(t, scriptedAdjudicator{verdict: MergeVerdictKeepDistinct})

	entry := consolidationFor(&lobslawv1.Cluster{
		Id:            "cluster-1",
		Records:       []*lobslawv1.VectorRecord{{Id: "a", Owner: "user:alice"}},
		AvgSimilarity: 0.97,
	}, MergeDecision{Verdict: MergeVerdictMerge, Reason: "duplicates"})
	entry.Applied, entry.Error = false, "refusing to merge cluster: members have different owners"
	d.recordConsolidation(entry)

	entries, err := ListConsolidations(store, ConsolidationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Applied {
		t.Error("a merge that failed is recorded as applied")
	}
	if e.Error == "" {
		t.Error("no error recorded; the log says it failed but not why")
	}
	if e.Verdict != "merge" {
		t.Errorf("verdict = %q; the decision that was made is still what it was", e.Verdict)
	}
}

// An adjudicator that errors must not produce a log entry claiming a
// verdict it never reached.
func TestAdjudicationFailureRecordsNothing(t *testing.T) {
	t.Parallel()
	d, store := dreamWithClusterOf(t, scriptedAdjudicator{err: errors.New("provider down")})

	if _, err := d.mergePhase(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := ListConsolidations(store, ConsolidationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed adjudication invented a verdict: %+v", entries)
	}
}

// A consolidation log that leaked across owners would describe one
// person's memories to another.
func TestListScopesByOwner(t *testing.T) {
	t.Parallel()
	store := freshStore(t)
	seedConsolidation(t, store, "a", "user:alice", "merge", time.Now())
	seedConsolidation(t, store, "b", "user:bob", "merge", time.Now())

	got, err := ListConsolidations(store, ConsolidationQuery{Owner: "user:alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Owner != "user:alice" {
		t.Errorf("owner filter returned %+v", got)
	}
}

func TestListFiltersAndOrders(t *testing.T) {
	t.Parallel()
	store := freshStore(t)
	now := time.Now()
	seedConsolidation(t, store, "old", "user:alice", "merge", now.Add(-72*time.Hour))
	seedConsolidation(t, store, "mid", "user:alice", "keep_distinct", now.Add(-2*time.Hour))
	seedConsolidation(t, store, "new", "user:alice", "merge", now)

	all, err := ListConsolidations(store, ConsolidationQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Id != "new" || all[2].Id != "old" {
		t.Errorf("not newest-first: %v", recordIDs(all))
	}

	recent, err := ListConsolidations(store, ConsolidationQuery{Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Errorf("since filter returned %v", recordIDs(recent))
	}

	merges, err := ListConsolidations(store, ConsolidationQuery{Verdict: "merge"})
	if err != nil {
		t.Fatal(err)
	}
	if len(merges) != 2 {
		t.Errorf("verdict filter returned %v", recordIDs(merges))
	}

	limited, err := ListConsolidations(store, ConsolidationQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Id != "new" {
		t.Errorf("limit returned %v; it should keep the newest", recordIDs(limited))
	}
}

func recordIDs(recs []*lobslawv1.ConsolidationRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Id)
	}
	return out
}

// --- helpers -------------------------------------------------------

// dreamWithClusterOf builds a raft-backed DreamRunner over two
// near-identical records owned by one person — the shape findClusters
// groups and the adjudicator is asked about.
func dreamWithClusterOf(t *testing.T, adj Adjudicator) (*DreamRunner, *Store) {
	t.Helper()
	node, fsm := newTestRaft(t)
	store := fsm.Store()
	now := time.Now()

	for _, id := range []string{"rec-a", "rec-b"} {
		putVectorRecord(t, store, &lobslawv1.VectorRecord{
			Id: id,
			// Identical embeddings: similarity 1.0, so they cluster
			// regardless of the threshold.
			Embedding: []float32{1, 0, 0},
			Text:      "alice likes tea",
			Retention: lobslawv1.Retention_RETENTION_LONG_TERM,
			Owner:     "user:alice",
			CreatedAt: timestamppb.New(now),
		})
	}

	d := &DreamRunner{
		raft:        node,
		store:       store,
		adjudicator: adj,
		cfg:         DreamConfig{Now: func() time.Time { return now }},
		logger:      slog.New(slog.DiscardHandler),
	}
	return d, store
}

func putVectorRecord(t *testing.T, s *Store, rec *lobslawv1.VectorRecord) {
	t.Helper()
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(BucketVectorRecords, rec.Id, raw); err != nil {
		t.Fatal(err)
	}
}

func freshStore(t *testing.T) *Store {
	t.Helper()
	s, _ := newTestStore(t)
	return s
}

func seedConsolidation(t *testing.T, s *Store, id, owner, verdict string, at time.Time) {
	t.Helper()
	rec := &lobslawv1.ConsolidationRecord{
		Id: id, ClusterId: id, Owner: owner, Verdict: verdict,
		CreatedAt: timestamppb.New(at),
	}
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(BucketConsolidations, id, raw); err != nil {
		t.Fatal(err)
	}
}
