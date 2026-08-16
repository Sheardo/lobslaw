package memory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Version history, so a refinement that turned out worse can be undone
// without the original having to be rewritten from memory.
//
// Bounded, because it is not free. Every version lives in the log and
// in every snapshot thereafter, on every node — "versioned artefacts"
// with no policy quietly becomes a store-growth problem that only
// shows up as slow snapshots months later.

// DefaultHistoryDepth is how many PRIOR versions are kept.
//
// Named for what it bounds. "keep_versions" does not say whether the
// active version counts toward it, which is the first question anybody
// asks — the active version is always kept and does not count.
const DefaultHistoryDepth = 10

// historyKey addresses one version. Zero-padded so a lexicographic
// bucket scan is also version order: without the padding, v10 sorts
// between v1 and v2 and "the oldest version" is whichever one happens
// to look smallest as a string.
func historyKey(id string, version uint32) string {
	return fmt.Sprintf("%s@%08d", id, version)
}

func historyPrefix(id string) string { return id + "@" }

// recordHistory snapshots a version before it is replaced, then prunes
// past the depth.
//
// Best-effort: a failed snapshot is logged by the caller and does not
// block the write it was recording. Losing a rollback point is a worse
// outcome than not writing the improvement, but only slightly — losing
// the improvement is what the user actually notices.
func (s *SelfTaughtStore) recordHistory(rec *lobslawv1.SelfTaughtRecord) error {
	if rec == nil || rec.Id == "" || rec.Version == 0 {
		return nil
	}
	snapshot := proto.Clone(rec).(*lobslawv1.SelfTaughtRecord)
	// The pending revision is not part of a version — it is a proposal
	// against one, and keeping it would make a rollback restore a
	// suggestion somebody had already declined to accept.
	snapshot.Pending = nil
	snapshot.Id = historyKey(rec.Id, rec.Version)

	if err := s.applyEntry(&lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_PUT,
		Id:      snapshot.Id,
		Payload: &lobslawv1.LogEntry_SelfTaughtHistory{SelfTaughtHistory: snapshot},
	}); err != nil {
		return err
	}
	return s.pruneHistory(rec.Id)
}

// History lists an artefact's prior versions, newest first.
func (s *SelfTaughtStore) History(id string) ([]*lobslawv1.SelfTaughtRecord, error) {
	var out []*lobslawv1.SelfTaughtRecord
	err := s.store.ForEachPrefix(BucketSelfTaughtHistory, historyPrefix(id),
		func(_ string, raw []byte) error {
			var rec lobslawv1.SelfTaughtRecord
			if err := proto.Unmarshal(raw, &rec); err != nil {
				return nil //nolint:nilerr // one unreadable version must not hide the rest
			}
			out = append(out, &rec)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("self-taught: history %s: %w", id, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// Rollback restores a prior version as the active one.
//
// The current version is snapshotted first, so rolling back is itself
// undoable — an operator who rolls back to the wrong version should
// not have destroyed the one they were on.
func (s *SelfTaughtStore) Rollback(ctx context.Context, id string, version uint32) (*lobslawv1.SelfTaughtRecord, error) {
	current, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	raw, err := s.store.Get(BucketSelfTaughtHistory, historyKey(id, version))
	if err != nil {
		return nil, fmt.Errorf("self-taught: %s has no version %d in history", id, version)
	}
	var target lobslawv1.SelfTaughtRecord
	if err := proto.Unmarshal(raw, &target); err != nil {
		return nil, fmt.Errorf("self-taught: decode version %d: %w", version, err)
	}
	if err := s.recordHistory(current); err != nil {
		return nil, err
	}

	restored := proto.Clone(current).(*lobslawv1.SelfTaughtRecord)
	restored.Body = target.Body
	restored.Files = target.Files
	restored.Description = target.Description
	restored.Embedding = target.Embedding
	// A new version number rather than the old one. Reusing it would
	// make two different records share a version, and the history
	// would no longer be a sequence anybody can reason about.
	restored.Version = current.Version + 1
	restored.Pending = nil
	if err := s.put(ctx, restored); err != nil {
		return nil, err
	}
	return restored, nil
}

// pruneHistory drops versions past the configured depth.
func (s *SelfTaughtStore) pruneHistory(id string) error {
	versions, err := s.History(id)
	if err != nil {
		return err
	}
	depth := s.historyDepth
	if depth <= 0 {
		depth = DefaultHistoryDepth
	}
	if len(versions) <= depth {
		return nil
	}
	for _, old := range versions[depth:] {
		key := historyKey(id, old.Version)
		if err := s.applyEntry(&lobslawv1.LogEntry{
			Op:      lobslawv1.LogOp_LOG_OP_DELETE,
			Id:      key,
			Payload: &lobslawv1.LogEntry_SelfTaughtHistory{SelfTaughtHistory: &lobslawv1.SelfTaughtRecord{Id: key}},
		}); err != nil {
			return fmt.Errorf("self-taught: prune %s: %w", key, err)
		}
	}
	return nil
}

// parseHistoryKey splits a history key back into its parts. Used by
// the offline CLI, which reads the bucket directly.
func parseHistoryKey(key string) (id string, version uint32, ok bool) {
	at := strings.LastIndex(key, "@")
	if at < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseUint(key[at+1:], 10, 32)
	if err != nil {
		return "", 0, false
	}
	return key[:at], uint32(n), true
}
