package memory

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The curator: what happens to a self-taught artefact that stops being
// used.
//
// A library that only grows is a library nobody can navigate, and
// every ACTIVE skill costs tokens on every turn whether or not it is
// ever read. But the answer is not deletion. For a product whose pitch
// is trust, an agent that can silently erase evidence of what it
// taught itself is the wrong default — so this only ever moves an
// artefact along a lifecycle that ends in the archive, from which
// anything can be restored.
//
// Deterministic and always on. hermes ships the umbrella-building
// consolidation fork off by default and keeps the inactivity prune
// running, and that split is right: a state transition on a threshold
// is cheap and predictable, while merging two artefacts is a
// judgement call that should be somebody's choice.

// Curation thresholds. Days rather than hours, because the thing being
// measured is a habit and a fortnight of holiday is not evidence that
// a skill is dead.
const (
	// DefaultStaleAfterDays is how long an artefact goes unused before
	// it is marked STALE.
	//
	// STALE still loads. It is a candidate for archiving, not a
	// removal — and the difference is the whole design, because an
	// artefact that stopped loading the moment it went stale could
	// never be used again, and the transition to ARCHIVED would be a
	// ratchet with no possible reprieve. Marking it and leaving it in
	// service is what makes the next 60 days a real second chance.
	DefaultStaleAfterDays = 30

	// DefaultArchiveAfterDays is the total unused age at which an
	// artefact leaves the live set. Measured from last use, not from
	// the stale transition, so the two thresholds are answers to the
	// same question rather than a sum somebody has to compute.
	DefaultArchiveAfterDays = 90

	// DefaultCurateInterval is how often the pass runs. Daily: the
	// thresholds are in days, so a faster tick could not change any
	// outcome and would only add raft traffic.
	DefaultCurateInterval = 24 * time.Hour
)

// CuratorConfig tunes the pass. Zero values take the defaults above.
type CuratorConfig struct {
	StaleAfterDays   int
	ArchiveAfterDays int
	Interval         time.Duration
}

func (c CuratorConfig) staleAfter() time.Duration {
	if c.StaleAfterDays <= 0 {
		return DefaultStaleAfterDays * 24 * time.Hour
	}
	return time.Duration(c.StaleAfterDays) * 24 * time.Hour
}

func (c CuratorConfig) archiveAfter() time.Duration {
	d := DefaultArchiveAfterDays * 24 * time.Hour
	if c.ArchiveAfterDays > 0 {
		d = time.Duration(c.ArchiveAfterDays) * 24 * time.Hour
	}
	// An archive threshold below the stale one would archive things
	// that had never been marked, which is not a configuration anybody
	// means. Clamped rather than refused: a node that will not boot
	// because two numbers are the wrong way round is a worse outcome
	// than one that curates slightly later than asked.
	if s := c.staleAfter(); d < s {
		return s
	}
	return d
}

func (c CuratorConfig) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultCurateInterval
	}
	return c.Interval
}

// CurationResult reports one pass.
type CurationResult struct {
	Staled   []string
	Archived []string
	// Revived names artefacts that were STALE and have been used
	// since. Counted because it is the evidence that STALE is a
	// warning rather than a one-way door.
	Revived []string
}

// Curate runs one pass over the live self-taught set.
//
// Reads BucketSelfTaught and BucketSelfTaughtUsage and writes through
// this store's own methods, so it cannot reach anything else. That is
// a property of what it can name, not a rule it follows: there is no
// path from here to episodic memory, to an operator skill, or to
// anything on disk.
func (s *SelfTaughtStore) Curate(ctx context.Context, cfg CuratorConfig) (CurationResult, error) {
	var res CurationResult
	live, err := s.List(SelfTaughtQuery{})
	if err != nil {
		return res, err
	}
	now := time.Now()
	staleAfter, archiveAfter := cfg.staleAfter(), cfg.archiveAfter()

	for _, rec := range live {
		// Pinned opts out of every automatic transition. Somebody who
		// has decided an artefact is worth keeping should not have to
		// defend it from the curator every fortnight.
		if rec.Pinned {
			continue
		}
		// PROPOSED is deliberately untouched. Archiving a proposal
		// nobody has looked at converts "not reviewed yet" into
		// "declined" — and the pending queue is the operator's inbox,
		// not the curator's to empty.
		switch rec.State {
		case lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE,
			lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE:
		default:
			continue
		}

		idle := now.Sub(s.lastActivity(rec))
		switch {
		case idle >= archiveAfter:
			reason := fmt.Sprintf("unused for %d days", int(idle.Hours()/24))
			if err := s.Archive(ctx, rec.Id, reason); err != nil {
				return res, err
			}
			res.Archived = append(res.Archived, rec.Id)

		case idle >= staleAfter:
			if rec.State == lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE {
				continue
			}
			if err := s.setState(ctx, rec, lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE); err != nil {
				return res, err
			}
			res.Staled = append(res.Staled, rec.Id)

		case rec.State == lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_STALE:
			// Used again inside the window. Back to ACTIVE, or STALE
			// would be permanent for anything seasonal — a skill for
			// the quarterly report is idle for eleven weeks by nature.
			if err := s.setState(ctx, rec, lobslawv1.SelfTaughtState_SELF_TAUGHT_STATE_ACTIVE); err != nil {
				return res, err
			}
			res.Revived = append(res.Revived, rec.Id)
		}
	}
	return res, nil
}

// lastActivity is when an artefact was last relevant.
//
// Usage first, from the raft-replicated counters — a curator computing
// staleness from one node's view would archive skills another node
// uses daily, which is why usage is a bucket rather than a per-process
// sidecar.
//
// Falling back to when it was APPROVED rather than when it was
// created, because the clock on "has anybody used this" cannot start
// before it was possible to. A skill proposed three months ago and
// approved yesterday is one day old for this purpose.
func (s *SelfTaughtStore) lastActivity(rec *lobslawv1.SelfTaughtRecord) time.Time {
	var latest time.Time
	consider := func(ts *timestamppb.Timestamp) {
		if ts == nil {
			return
		}
		if t := ts.AsTime(); t.After(latest) {
			latest = t
		}
	}
	consider(s.usageFor(rec.Id).LastUsedAt)
	consider(rec.ApprovedAt)
	// UpdatedAt catches a refinement: an artefact somebody improved
	// last week is being maintained, whatever the usage counter says.
	//
	// This is why setState leaves UpdatedAt alone. If a lifecycle
	// transition counted as an edit, marking something STALE would
	// reset the very clock that decides whether it archives, and
	// nothing would ever leave the live set.
	consider(rec.UpdatedAt)
	if latest.IsZero() {
		consider(rec.CreatedAt)
	}
	if latest.IsZero() {
		// No timestamps at all. Treated as brand new rather than
		// infinitely old, because the alternative archives every
		// record written before whichever field went missing.
		return time.Now()
	}
	return latest
}

// setState moves an artefact along the lifecycle.
func (s *SelfTaughtStore) setState(ctx context.Context, rec *lobslawv1.SelfTaughtRecord, state lobslawv1.SelfTaughtState) error {
	updated := proto.Clone(rec).(*lobslawv1.SelfTaughtRecord)
	updated.State = state
	// UpdatedAt is deliberately untouched: it records when the
	// ARTEFACT last changed, and moving one along the lifecycle is not
	// a change to it. See lastActivity for why that matters.
	return s.put(ctx, updated)
}

// CurateLoop runs Curate on a ticker until ctx is done.
//
// Leader-gated by the caller, not here. Every node running it would be
// correct — the transitions are idempotent and go through raft — but
// it would burn a round trip per node per transition to reach the same
// answer.
func (s *SelfTaughtStore) CurateLoop(ctx context.Context, cfg CuratorConfig, log logger) error {
	t := time.NewTicker(cfg.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			res, err := s.Curate(ctx, cfg)
			if err != nil {
				log.Warn("self-taught: curation pass failed", "err", err)
				continue
			}
			// Logged only when something moved. A daily "curated 0
			// artefacts" is noise that teaches an operator to filter
			// out the line that matters.
			if len(res.Staled)+len(res.Archived)+len(res.Revived) > 0 {
				log.Info("self-taught: curated",
					"staled", res.Staled, "archived", res.Archived, "revived", res.Revived)
			}
		}
	}
}

// logger is the slice of slog this file needs, so the store does not
// grow a logger field for one loop.
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}
