package memory

import (
	"math"
	"testing"

	"google.golang.org/protobuf/proto"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// VectorScanEntry reads VectorRecord bytes by sharing its field numbers.
// A renumbered field decodes the wrong data rather than failing, so pin
// it: marshal a record, decode it as the view, compare.
func TestVectorScanEntryMatchesVectorRecordWireFormat(t *testing.T) {
	t.Parallel()
	rec := &lobslawv1.VectorRecord{
		Id:         "vec-1",
		Embedding:  []float32{0.25, -0.5, 0.75},
		Text:       "text the scan must never decode",
		Metadata:   map[string]string{"k": "v"},
		Scope:      "episodic",
		Retention:  lobslawv1.Retention_RETENTION_LONG_TERM,
		SourceIds:  []string{"src-1"},
		Owner:      "user:alice",
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		Norm:       1.25,
	}
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}

	var got lobslawv1.VectorScanEntry
	if err := scanUnmarshal.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode as view: %v", err)
	}

	if len(got.Embedding) != len(rec.Embedding) {
		t.Fatalf("embedding len = %d, want %d", len(got.Embedding), len(rec.Embedding))
	}
	for i := range rec.Embedding {
		if got.Embedding[i] != rec.Embedding[i] {
			t.Errorf("embedding[%d] = %v, want %v", i, got.Embedding[i], rec.Embedding[i])
		}
	}
	if got.Scope != rec.Scope {
		t.Errorf("scope = %q, want %q", got.Scope, rec.Scope)
	}
	if got.Retention != rec.Retention {
		t.Errorf("retention = %v, want %v", got.Retention, rec.Retention)
	}
	if got.Owner != rec.Owner {
		t.Errorf("owner = %q, want %q", got.Owner, rec.Owner)
	}
	if got.Visibility != rec.Visibility {
		t.Errorf("visibility = %v, want %v", got.Visibility, rec.Visibility)
	}
	if got.Norm != rec.Norm {
		t.Errorf("norm = %v, want %v", got.Norm, rec.Norm)
	}

	// The point of the view: skipped fields cost nothing to carry.
	if n := len(got.ProtoReflect().GetUnknown()); n != 0 {
		t.Errorf("view retained %d bytes of unknown fields; DiscardUnknown not applied", n)
	}
}

func TestTopK(t *testing.T) {
	t.Parallel()

	t.Run("keeps the highest scores", func(t *testing.T) {
		t.Parallel()
		top := newTopK(3)
		for _, c := range []struct {
			key   string
			score float32
		}{
			{"a", 0.1}, {"b", 0.9}, {"c", 0.5}, {"d", 0.95}, {"e", 0.2}, {"f", 0.7},
		} {
			top.push(c.key, c.score)
		}
		got := top.sorted()
		want := []string{"d", "b", "f"}
		if len(got) != len(want) {
			t.Fatalf("got %d candidates, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].key != want[i] {
				t.Errorf("position %d = %q, want %q", i, got[i].key, want[i])
			}
		}
	})

	t.Run("fewer pushes than limit", func(t *testing.T) {
		t.Parallel()
		top := newTopK(5)
		top.push("a", 0.2)
		top.push("b", 0.4)
		got := top.sorted()
		if len(got) != 2 || got[0].key != "b" {
			t.Fatalf("got %+v, want [b a]", got)
		}
	})

	t.Run("equal scores break on key for stable ordering", func(t *testing.T) {
		t.Parallel()
		top := newTopK(2)
		top.push("z", 0.5)
		top.push("a", 0.5)
		got := top.sorted()
		if got[0].key != "a" || got[1].key != "z" {
			t.Errorf("got [%s %s], want [a z]", got[0].key, got[1].key)
		}
	})

	t.Run("NaN never displaces a real score", func(t *testing.T) {
		t.Parallel()
		top := newTopK(2)
		top.push("a", 0.3)
		top.push("b", 0.6)
		top.push("nan", float32(math.NaN()))
		for _, c := range top.sorted() {
			if c.key == "nan" {
				t.Fatal("NaN candidate displaced a real score")
			}
		}
	})
}

// Records written before VectorRecord.norm existed carry zero, and must
// still score correctly from the computed fallback.
func TestVectorSearchNormFallback(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)

	put := func(id string, emb []float32, storedNorm float32) {
		raw, err := proto.Marshal(&lobslawv1.VectorRecord{
			Id: id, Embedding: emb, Norm: storedNorm,
			Visibility: lobslawv1.Visibility_VISIBILITY_SHARED,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Put(BucketVectorRecords, id, raw); err != nil {
			t.Fatal(err)
		}
	}
	// Identical vectors, one with the norm stored and one without.
	put("with-norm", []float32{3, 4, 0}, 5)
	put("without-norm", []float32{3, 4, 0}, 0)

	hits, err := vectorSearch(s, []float32{3, 4, 0}, 2, Everyone(), "",
		lobslawv1.Retention_RETENTION_UNSPECIFIED)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	for _, h := range hits {
		if math.Abs(float64(h.Score())-1.0) > 1e-6 {
			t.Errorf("%s scored %v, want ~1.0", h.Record().Id, h.Score())
		}
	}
}

// The FSM computes norm so no producer can forget it.
func TestFSMApplyPutComputesVectorNorm(t *testing.T) {
	t.Parallel()
	_, fsm := newTestRaft(t)

	entry := &lobslawv1.LogEntry{
		Op: lobslawv1.LogOp_LOG_OP_PUT,
		Id: "vec-1",
		Payload: &lobslawv1.LogEntry_VectorRecord{
			VectorRecord: &lobslawv1.VectorRecord{
				Id:        "vec-1",
				Embedding: []float32{3, 4, 0},
			},
		},
	}
	if err := fsm.applyPut(entry); err != nil {
		t.Fatal(err)
	}

	raw, err := fsm.store.Get(BucketVectorRecords, "vec-1")
	if err != nil {
		t.Fatal(err)
	}
	var got lobslawv1.VectorRecord
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(got.Norm)-5.0) > 1e-6 {
		t.Errorf("stored norm = %v, want 5", got.Norm)
	}
}
