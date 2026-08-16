package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Provenance by location, not by tag.
//
// The alternative — one store with an "authored by the agent" marker —
// makes every consumer responsible for remembering the distinction. A
// curator restricted to agent-written artefacts has to apply that as a
// rule; a review fork has to be told in its prompt what is off-limits;
// and "forget everything you taught yourself" becomes a query somebody
// has to get right.
//
// A separate store makes all of it structural. If a record is here,
// the agent wrote it. There is no marker to forget, forge, or lose.

// SelfLearningMode is the three-state switch. Two states would be the
// wrong shape for a security-first product: "on" and "off" leaves no
// room for "write it down but do not act on it until I have looked",
// which is the setting most people actually want.
type SelfLearningMode string

const (
	// SelfLearningOff means the store is never wired. Verifiable by
	// absence rather than by a branch somebody has to have remembered
	// to check — "the capability is not present" is a different and
	// stronger claim than "the call sites are guarded".
	SelfLearningOff SelfLearningMode = "off"

	// SelfLearningPropose lands artefacts PROPOSED and inert. The
	// right default.
	SelfLearningPropose SelfLearningMode = "propose"

	// SelfLearningAuto activates artefacts immediately.
	SelfLearningAuto SelfLearningMode = "auto"
)

// ParseSelfLearningMode reads the config value. Anything unrecognised
// — including the empty string — is "off": a typo in this setting must
// never be the reason an agent started writing its own instructions.
func ParseSelfLearningMode(s string) SelfLearningMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(SelfLearningPropose):
		return SelfLearningPropose
	case string(SelfLearningAuto):
		return SelfLearningAuto
	default:
		return SelfLearningOff
	}
}

var (
	// ErrNotProposed is returned when approving something that is not
	// awaiting approval.
	ErrNotProposed = errors.New("self-taught: artefact is not awaiting approval")

	// ErrArtefactNotFound is returned for an unknown id.
	ErrArtefactNotFound = errors.New("self-taught: not found")
)

// SelfTaughtStore holds what the agent wrote for itself.
type SelfTaughtStore struct {
	raft  raftApplier
	store *Store
	mode  SelfLearningMode

	// usage is batched in-process. Counter bumps are high-frequency
	// and low-value, and paying consensus for each one is the obvious
	// way to make this worse than the sidecar file it replaces.
	// Losing a handful of counts to a crash is acceptable; losing the
	// write path to contention is not.
	usageMu sync.Mutex
	usage   map[string]*pendingUsage
}

type pendingUsage struct {
	delta    uint64
	lastUsed time.Time
}

// NewSelfTaughtStore builds the store. Returns nil, nil when the mode
// is off — the caller wires nothing, and every dependent is absent
// rather than guarded.
func NewSelfTaughtStore(raft raftApplier, store *Store, mode SelfLearningMode) (*SelfTaughtStore, error) {
	if mode == SelfLearningOff {
		return nil, nil
	}
	if raft == nil || store == nil {
		return nil, errors.New("self-taught: Raft and Store are both required")
	}
	return &SelfTaughtStore{
		raft:  raft,
		store: store,
		mode:  mode,
		usage: map[string]*pendingUsage{},
	}, nil
}

// Mode reports the configured mode. Never off on a live store: an off
// deployment has no store at all.
func (s *SelfTaughtStore) Mode() SelfLearningMode { return s.mode }

// Propose records a new artefact.
//
// The initial state comes from the mode, not from the caller. A caller
// that could choose would eventually choose ACTIVE somewhere, and the
// operator's setting would quietly stop meaning what it says.
func (s *SelfTaughtStore) Propose(ctx context.Context, rec *lobslawv1.SelfTaughtRecord) (*lobslawv1.SelfTaughtRecord, error) {
	if rec == nil {
		return nil, errors.New("self-taught: nil record")
	}
	if strings.TrimSpace(rec.Name) == "" {
		return nil, errors.New("self-taught: name is required")
	}
	if rec.Kind == lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_UNSPECIFIED {
		return nil, errors.New("self-taught: kind is required")
	}

	out := proto.Clone(rec).(*lobslawv1.SelfTaughtRecord)
	if out.Id == "" {
		out.Id = kindSlug(out.Kind) + ":" + out.Name
	}
	out.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED
	if s.mode == SelfLearningAuto {
		out.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE
	}
	now := timestamppb.Now()
	out.CreatedAt, out.UpdatedAt = now, now
	out.Version = 1

	if existing, err := s.Get(out.Id); err == nil {
		// A re-proposal is a new version of the same artefact, not a
		// second one. Overwriting the version would make the history
		// R18 builds on start again from 1 every time the agent
		// changed its mind.
		out.Version = existing.Version + 1
		out.CreatedAt = existing.CreatedAt
	}

	if err := s.put(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Approve moves a PROPOSED artefact to ACTIVE.
func (s *SelfTaughtStore) Approve(ctx context.Context, id, by string) (*lobslawv1.SelfTaughtRecord, error) {
	current, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if current.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED {
		return nil, fmt.Errorf("%w: %s is %s", ErrNotProposed, id, stateName(current.State))
	}
	updated := proto.Clone(current).(*lobslawv1.SelfTaughtRecord)
	updated.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE
	updated.ApprovedBy = by
	updated.ApprovedAt = timestamppb.Now()
	updated.UpdatedAt = updated.ApprovedAt
	if err := s.put(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// Archive moves an artefact out of the live set, recoverably.
//
// Deletion is not a lifecycle transition. For a product whose pitch is
// trust, an agent that can silently erase evidence of what it taught
// itself is the wrong default.
//
// Two writes rather than one, because the FSM routes by state and a
// record cannot be in two buckets atomically. A crash between them
// leaves it in both; List filters ARCHIVED out of the live set, so the
// duplicate is invisible and the next archive of the same id clears
// it. The failure mode is a stale copy nobody reads, which is the
// right side to err on for a store whose promise is that nothing is
// lost.
func (s *SelfTaughtStore) Archive(ctx context.Context, id, reason string) error {
	current, err := s.Get(id)
	if err != nil {
		return err
	}
	if current.Pinned {
		return fmt.Errorf("self-taught: %s is pinned; unpin it before archiving", id)
	}
	archived := proto.Clone(current).(*lobslawv1.SelfTaughtRecord)
	archived.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED
	archived.ArchivedReason = reason
	archived.UpdatedAt = timestamppb.Now()

	if err := s.put(ctx, archived); err != nil {
		return err
	}
	return s.applyEntry(&lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_DELETE,
		Id:      id,
		Payload: &lobslawv1.LogEntry_SelfTaught{SelfTaught: &lobslawv1.SelfTaughtRecord{Id: id}},
	})
}

// Restore brings an archived artefact back, PROPOSED.
//
// Not ACTIVE, even if it was active when archived: something archived
// itself out of use once, and putting it straight back in force skips
// the decision that archiving implied.
func (s *SelfTaughtStore) Restore(ctx context.Context, id string) (*lobslawv1.SelfTaughtRecord, error) {
	raw, err := s.store.Get(BucketSelfTaughtArchive, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not archived", ErrArtefactNotFound, id)
	}
	var rec lobslawv1.SelfTaughtRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("self-taught: decode: %w", err)
	}
	restored := proto.Clone(&rec).(*lobslawv1.SelfTaughtRecord)
	restored.State = lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED
	restored.ArchivedReason = ""
	restored.UpdatedAt = timestamppb.Now()
	if err := s.put(ctx, restored); err != nil {
		return nil, err
	}
	return restored, nil
}

// Get reads a live artefact.
func (s *SelfTaughtStore) Get(id string) (*lobslawv1.SelfTaughtRecord, error) {
	raw, err := s.store.Get(BucketSelfTaught, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrArtefactNotFound, id)
	}
	var rec lobslawv1.SelfTaughtRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("self-taught: decode %s: %w", id, err)
	}
	return &rec, nil
}

// SelfTaughtQuery filters a listing.
type SelfTaughtQuery struct {
	Kind  lobslawv1.SelfTaughtKind
	State lobslawv1.SelfTaughtState
	Owner string
	// Archived reads the archive instead of the live set.
	Archived bool
}

// List reads the store, newest first.
func (s *SelfTaughtStore) List(q SelfTaughtQuery) ([]*lobslawv1.SelfTaughtRecord, error) {
	bucket := BucketSelfTaught
	if q.Archived {
		bucket = BucketSelfTaughtArchive
	}
	var out []*lobslawv1.SelfTaughtRecord
	err := s.store.ForEach(bucket, func(_ string, raw []byte) error {
		var rec lobslawv1.SelfTaughtRecord
		if err := proto.Unmarshal(raw, &rec); err != nil {
			return nil //nolint:nilerr // one unreadable record must not hide the rest
		}
		// An archived record that survived a partial Archive is
		// filtered out of the live set here, which is what makes the
		// two-write archive safe.
		if !q.Archived && rec.State == lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED {
			return nil
		}
		if q.Kind != lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_UNSPECIFIED && rec.Kind != q.Kind {
			return nil
		}
		if q.State != lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_UNSPECIFIED && rec.State != q.State {
			return nil
		}
		if q.Owner != "" && rec.Owner != q.Owner {
			return nil
		}
		out = append(out, &rec)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("self-taught: list: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		return recordTime(out[i]).After(recordTime(out[j]))
	})
	return out, nil
}

// Active lists what is currently in force — the set a loader should
// materialise.
func (s *SelfTaughtStore) Active(kind lobslawv1.SelfTaughtKind) ([]*lobslawv1.SelfTaughtRecord, error) {
	return s.List(SelfTaughtQuery{
		Kind:  kind,
		State: lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE,
	})
}

// DiscardAll archives everything live. The "forget everything you
// taught yourself" operation, which is one scan precisely because
// provenance is a location.
func (s *SelfTaughtStore) DiscardAll(ctx context.Context, reason string) (int, error) {
	live, err := s.List(SelfTaughtQuery{})
	if err != nil {
		return 0, err
	}
	var n int
	for _, rec := range live {
		if rec.Pinned {
			continue
		}
		if err := s.Archive(ctx, rec.Id, reason); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RecordUse bumps an in-process counter. Nothing reaches raft until
// FlushUsage.
func (s *SelfTaughtStore) RecordUse(id string) {
	if s == nil || id == "" {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	p, ok := s.usage[id]
	if !ok {
		p = &pendingUsage{}
		s.usage[id] = p
	}
	p.delta++
	p.lastUsed = time.Now()
}

// FlushUsage writes the batched counters. Called on turn end or on a
// timer; safe to call with nothing pending.
func (s *SelfTaughtStore) FlushUsage(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	pending := s.usage
	s.usage = map[string]*pendingUsage{}
	s.usageMu.Unlock()

	for id, p := range pending {
		current := s.usageFor(id)
		current.Invocations += p.delta
		current.LastUsedAt = timestamppb.New(p.lastUsed)
		if current.FirstUsedAt == nil {
			current.FirstUsedAt = current.LastUsedAt
		}
		if err := s.applyEntry(&lobslawv1.LogEntry{
			Op:      lobslawv1.LogOp_LOG_OP_PUT,
			Id:      id,
			Payload: &lobslawv1.LogEntry_SelfTaughtUsage{SelfTaughtUsage: current},
		}); err != nil {
			// Counters are best-effort. Putting the batch back would
			// risk an unbounded queue behind a persistent failure, and
			// a lost count is a slightly-late staleness transition
			// rather than anything a user notices.
			return fmt.Errorf("self-taught: flush usage for %s: %w", id, err)
		}
	}
	return nil
}

// Usage reads the aggregated counters for an artefact.
func (s *SelfTaughtStore) Usage(id string) *lobslawv1.SelfTaughtUsage {
	return s.usageFor(id)
}

func (s *SelfTaughtStore) usageFor(id string) *lobslawv1.SelfTaughtUsage {
	raw, err := s.store.Get(BucketSelfTaughtUsage, id)
	if err != nil {
		return &lobslawv1.SelfTaughtUsage{Id: id}
	}
	var u lobslawv1.SelfTaughtUsage
	if err := proto.Unmarshal(raw, &u); err != nil {
		return &lobslawv1.SelfTaughtUsage{Id: id}
	}
	return &u
}

func (s *SelfTaughtStore) put(_ context.Context, rec *lobslawv1.SelfTaughtRecord) error {
	return s.applyEntry(&lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_PUT,
		Id:      rec.Id,
		Payload: &lobslawv1.LogEntry_SelfTaught{SelfTaught: rec},
	})
}

func (s *SelfTaughtStore) applyEntry(entry *lobslawv1.LogEntry) error {
	data, err := proto.Marshal(entry)
	if err != nil {
		return fmt.Errorf("self-taught: marshal: %w", err)
	}
	res, err := s.raft.Apply(data, 5*time.Second)
	if err != nil {
		return fmt.Errorf("self-taught: raft apply: %w", err)
	}
	if ferr, ok := res.(error); ok && ferr != nil {
		return ferr
	}
	return nil
}

func recordTime(r *lobslawv1.SelfTaughtRecord) time.Time {
	if r.UpdatedAt == nil {
		return time.Time{}
	}
	return r.UpdatedAt.AsTime()
}

func kindSlug(k lobslawv1.SelfTaughtKind) string {
	switch k {
	case lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_SKILL:
		return "skill"
	case lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_PROCEDURE:
		return "procedure"
	case lobslawv1.SelfTaughtKind_SELF_TAUGHT_KIND_PINNED_PROPOSAL:
		return "pinned"
	default:
		return "artefact"
	}
}

func stateName(s lobslawv1.SelfTaughtState) string {
	switch s {
	case lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_PROPOSED:
		return "proposed"
	case lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE:
		return "active"
	case lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE:
		return "stale"
	case lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ARCHIVED:
		return "archived"
	default:
		return "unknown"
	}
}
