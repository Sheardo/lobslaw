package memory

import (
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Dream merges, supersedes and prunes memories on its own schedule.
// The verdict and the reason were computed on every run and then
// written to a log line and forgotten, so a user asking "why did it
// merge those two notes" had no answer, and one asking "what has it
// been doing to my memory" had none either.
//
// Memory that silently rewrites itself and cannot be inspected is a
// trust problem for a privacy-first product. This is the record.

// consolidationRetention bounds the log. Long enough to answer "what
// happened to that note last week", short enough that a cluster
// re-adjudicated nightly does not accumulate a record per night
// forever.
const consolidationRetention = 90 * 24 * time.Hour

// maxConsolidationEntries is a second bound, for a deployment busy
// enough to produce more in 90 days than anybody would page through.
// Whichever bound bites first wins.
const maxConsolidationEntries = 5000

// recordConsolidation writes one adjudication to the log.
//
// Failure to write is logged and swallowed. The log is for
// transparency, and losing an entry is a worse outcome for the user
// than a failed write — but not worse than the consolidation itself
// being abandoned because its audit trail could not be saved.
func (d *DreamRunner) recordConsolidation(rec *lobslawv1.ConsolidationRecord) {
	if rec == nil {
		return
	}
	rec.CreatedAt = timestamppb.New(d.cfg.Now())
	if rec.Id == "" {
		rec.Id = fmt.Sprintf("%s-%d", rec.ClusterId, rec.CreatedAt.AsTime().UnixNano())
	}
	if err := d.applyEntry(&lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_PUT,
		Id:      rec.Id,
		Payload: &lobslawv1.LogEntry_Consolidation{Consolidation: rec},
	}); err != nil {
		d.logger.Warn("consolidation log: write failed",
			"cluster", rec.ClusterId, "verdict", rec.Verdict, "err", err)
	}
}

// consolidationFor builds the log entry for a decision, before it is
// known whether applying it succeeded.
func consolidationFor(c *lobslawv1.Cluster, decision MergeDecision) *lobslawv1.ConsolidationRecord {
	sourceIDs := make([]string, 0, len(c.Records))
	for _, r := range c.Records {
		sourceIDs = append(sourceIDs, r.Id)
	}
	owner := ""
	if len(c.Records) > 0 {
		owner = c.Records[0].Owner
	}
	return &lobslawv1.ConsolidationRecord{
		ClusterId:     c.Id,
		Verdict:       decision.Verdict.String(),
		Reason:        decision.Reason,
		SourceIds:     sourceIDs,
		MemberCount:   int32(len(c.Records)),
		AvgSimilarity: c.AvgSimilarity,
		Owner:         owner,
		Applied:       true,
	}
}

// ConsolidationQuery filters a read of the log.
type ConsolidationQuery struct {
	// Owner, when set, restricts to one principal's memories. Empty
	// reads everything, which is for the offline CLI — a live caller
	// must scope, or the log describes one person's memories to
	// another.
	Owner string
	// Verdict, when set, restricts to one kind of decision.
	Verdict string
	// Since, when non-zero, drops anything older.
	Since time.Time
	// Limit caps the result. Zero is unlimited.
	Limit int
}

// ListConsolidations reads the log, newest first.
func ListConsolidations(store *Store, q ConsolidationQuery) ([]*lobslawv1.ConsolidationRecord, error) {
	var out []*lobslawv1.ConsolidationRecord
	err := store.ForEach(BucketConsolidations, func(_ string, raw []byte) error {
		var rec lobslawv1.ConsolidationRecord
		if err := proto.Unmarshal(raw, &rec); err != nil {
			return nil //nolint:nilerr // one unreadable entry should not hide the rest
		}
		if q.Owner != "" && rec.Owner != q.Owner {
			return nil
		}
		if q.Verdict != "" && rec.Verdict != q.Verdict {
			return nil
		}
		if !q.Since.IsZero() && rec.CreatedAt != nil && rec.CreatedAt.AsTime().Before(q.Since) {
			return nil
		}
		out = append(out, &rec)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read consolidation log: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		return consolidationTime(out[i]).After(consolidationTime(out[j]))
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func consolidationTime(r *lobslawv1.ConsolidationRecord) time.Time {
	if r.CreatedAt == nil {
		return time.Time{}
	}
	return r.CreatedAt.AsTime()
}

// pruneConsolidations drops entries past either bound. Returns how
// many it removed.
//
// Run by Dream itself rather than by a separate janitor: the thing
// that writes the log is the thing that knows when it last ran, and a
// second scheduled task to tidy after the first is one more thing to
// misconfigure.
func (d *DreamRunner) pruneConsolidations() (int, error) {
	all, err := ListConsolidations(d.store, ConsolidationQuery{})
	if err != nil {
		return 0, err
	}
	cutoff := d.cfg.Now().Add(-consolidationRetention)

	var doomed []string
	for i, rec := range all {
		// Sorted newest-first, so anything past the count bound is
		// among the oldest.
		if i >= maxConsolidationEntries || consolidationTime(rec).Before(cutoff) {
			doomed = append(doomed, rec.Id)
		}
	}
	for _, id := range doomed {
		if err := d.applyEntry(&lobslawv1.LogEntry{
			Op: lobslawv1.LogOp_LOG_OP_DELETE,
			Id: id,
			Payload: &lobslawv1.LogEntry_Consolidation{
				Consolidation: &lobslawv1.ConsolidationRecord{Id: id},
			},
		}); err != nil {
			return 0, fmt.Errorf("prune consolidation %s: %w", id, err)
		}
	}
	return len(doomed), nil
}
