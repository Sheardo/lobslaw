package memory

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The offline face of the self-taught store, for `lobslaw learned`.
//
// Writes go to the bucket directly rather than through raft, because
// the node is stopped and there is no consensus to reach. Separate
// from SelfTaughtStore rather than a mode on it: a store that
// sometimes bypasses raft is one misconfiguration away from doing so
// while the cluster is running, and the two callers have nothing else
// in common.

// OfflineSelfTaught reads and edits the store with the node stopped.
type OfflineSelfTaught struct{ store *Store }

// NewOfflineSelfTaught wraps an already-open store.
func NewOfflineSelfTaught(store *Store) *OfflineSelfTaught {
	return &OfflineSelfTaught{store: store}
}

// List reads live or archived artefacts, newest first.
func (o *OfflineSelfTaught) List(archived bool, owner string) ([]*lobslawv1.SelfTaughtRecord, error) {
	bucket := BucketSelfTaught
	if archived {
		bucket = BucketSelfTaughtArchive
	}
	var out []*lobslawv1.SelfTaughtRecord
	err := o.store.ForEach(bucket, func(_ string, raw []byte) error {
		var rec lobslawv1.SelfTaughtRecord
		if err := proto.Unmarshal(raw, &rec); err != nil {
			return nil //nolint:nilerr // one unreadable record must not hide the rest
		}
		if !archived && rec.State == lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED {
			return nil
		}
		if owner != "" && rec.Owner != owner {
			return nil
		}
		out = append(out, &rec)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("learned: list: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

// Find locates an artefact in either bucket, reporting which.
func (o *OfflineSelfTaught) Find(id string) (rec *lobslawv1.SelfTaughtRecord, archived bool, err error) {
	if r, e := o.read(BucketSelfTaught, id); e == nil {
		return r, false, nil
	}
	if r, e := o.read(BucketSelfTaughtArchive, id); e == nil {
		return r, true, nil
	}
	return nil, false, fmt.Errorf("%w: %s", ErrArtefactNotFound, id)
}

// Archive moves an artefact out of the live set. Refuses a pinned one
// for the same reason the online store does.
func (o *OfflineSelfTaught) Archive(rec *lobslawv1.SelfTaughtRecord, reason string) error {
	if rec.Pinned {
		return fmt.Errorf("learned: %s is pinned; unpin it before archiving", rec.Id)
	}
	archived := proto.Clone(rec).(*lobslawv1.SelfTaughtRecord)
	archived.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED
	archived.ArchivedReason = reason
	archived.UpdatedAt = timestamppb.Now()
	if err := o.write(BucketSelfTaughtArchive, archived); err != nil {
		return err
	}
	return o.store.Delete(BucketSelfTaught, rec.Id)
}

// Restore brings an archived artefact back as a proposal — not
// active, for the same reason the online store gives: archiving
// implied a decision, and restoring straight into force skips it.
func (o *OfflineSelfTaught) Restore(rec *lobslawv1.SelfTaughtRecord) error {
	restored := proto.Clone(rec).(*lobslawv1.SelfTaughtRecord)
	restored.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED
	restored.ArchivedReason = ""
	restored.UpdatedAt = timestamppb.Now()
	if err := o.write(BucketSelfTaught, restored); err != nil {
		return err
	}
	return o.store.Delete(BucketSelfTaughtArchive, rec.Id)
}

// Usage reads the aggregated counters.
func (o *OfflineSelfTaught) Usage(id string) *lobslawv1.SelfTaughtUsage {
	raw, err := o.store.Get(BucketSelfTaughtUsage, id)
	if err != nil {
		return &lobslawv1.SelfTaughtUsage{Id: id}
	}
	var u lobslawv1.SelfTaughtUsage
	if err := proto.Unmarshal(raw, &u); err != nil {
		return &lobslawv1.SelfTaughtUsage{Id: id}
	}
	return &u
}

func (o *OfflineSelfTaught) read(bucket, id string) (*lobslawv1.SelfTaughtRecord, error) {
	raw, err := o.store.Get(bucket, id)
	if err != nil {
		return nil, err
	}
	var rec lobslawv1.SelfTaughtRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (o *OfflineSelfTaught) write(bucket string, rec *lobslawv1.SelfTaughtRecord) error {
	raw, err := proto.Marshal(rec)
	if err != nil {
		return fmt.Errorf("learned: marshal %s: %w", rec.Id, err)
	}
	return o.store.Put(bucket, rec.Id, raw)
}
