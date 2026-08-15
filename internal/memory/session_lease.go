package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// A turn is: load history, run the agent for seconds with tool loops,
// append the result. Two turns on one conversation must not overlap,
// or both read the same prior history and both append it.
//
// The gateway's TurnGate serialises turns inside a process. This is
// the part that holds across nodes, which the in-process gate cannot:
// a conversation is reachable from more than one gateway at a time —
// a webhook and a REST client, or two gateways behind a load
// balancer — and those are different processes with different maps.

var (
	// ErrLeaseHeld means another node is running a turn on this
	// conversation. Retryable: the holder will release, or its lease
	// will expire.
	ErrLeaseHeld = errors.New("session lease: held by another node")

	// ErrLeaseLost means this node no longer holds the lease it
	// thought it held — its claim expired and someone took over. The
	// turn should stop rather than write a result nobody is waiting
	// for on top of whatever the new holder is doing.
	ErrLeaseLost = errors.New("session lease: no longer held by this node")
)

// DefaultLeaseTTL bounds how long a conversation stays locked to a
// node that has stopped responding. It matches the gateway's
// responsiveness hard timeout: a turn that outlives that is already
// being force-summarised, so a lease outliving it would only block
// the user's next message.
const DefaultLeaseTTL = 90 * time.Second

// LeaseService issues cluster-wide turn leases.
type LeaseService struct {
	raft   *RaftNode
	store  *Store
	nodeID string
	ttl    time.Duration
	log    *slog.Logger
	now    func() time.Time
}

// LeaseConfig configures the service. Zero TTL takes DefaultLeaseTTL;
// Now is injectable so expiry can be tested without sleeping.
type LeaseConfig struct {
	NodeID string
	TTL    time.Duration
	Logger *slog.Logger
	Now    func() time.Time
}

// NewLeaseService builds the service. A nil raft yields a service
// whose Acquire always succeeds without writing anything — the
// single-node-without-raft case, where there is no second node to
// race with and refusing would take the gateway offline.
func NewLeaseService(raft *RaftNode, store *Store, cfg LeaseConfig) *LeaseService {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultLeaseTTL
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LeaseService{
		raft: raft, store: store,
		nodeID: cfg.NodeID, ttl: cfg.TTL,
		log: cfg.Logger, now: cfg.Now,
	}
}

// SessionLease is a held lease. Heartbeat extends it; Release drops
// it. Both are safe to call on a nil lease, so a caller that could
// not acquire one does not need to branch.
type SessionLease struct {
	svc *LeaseService
	key string

	mu       sync.Mutex
	rec      *lobslawv1.SessionLease
	released bool
}

// Acquire takes the lease for one turn, or reports who holds it.
//
// An expired lease is taken over: the previous holder is presumed
// dead, and a conversation that stays locked to a crashed node is
// worse than the small chance of overlapping with a node that has
// merely stalled — which is what the TTL is chosen to bound.
func (s *LeaseService) Acquire(ctx context.Context, key, turnID string) (*SessionLease, error) {
	if s == nil || s.raft == nil {
		return nil, nil
	}
	if key == "" {
		return nil, errors.New("session lease: empty key")
	}

	now := s.now()
	current, err := s.load(key)
	if err != nil {
		return nil, err
	}

	var expectedClaimer string
	var expectedRev uint64
	if current != nil {
		expectedRev = current.Revision
		holder := current.ClaimedBy
		if holder != "" && holder != s.nodeID {
			// Expiry is evaluated here rather than in the FSM: it is
			// wall-clock, and the FSM must replay identically on every
			// replica. Taking over therefore names the dead holder
			// explicitly, so the CAS stays an exact comparison.
			if exp := current.GetClaimExpiresAt(); exp != nil && exp.AsTime().After(now) {
				return nil, fmt.Errorf("%w: %s holds %q until %s",
					ErrLeaseHeld, holder, key, exp.AsTime().Format(time.RFC3339))
			}
			s.log.Info("session lease: taking over an expired lease",
				"key", key, "previous_holder", holder)
		}
		expectedClaimer = holder
	}

	rec := &lobslawv1.SessionLease{
		Id:             key,
		ClaimedBy:      s.nodeID,
		ClaimExpiresAt: timestamppb.New(now.Add(s.ttl)),
		TurnId:         turnID,
	}
	if err := s.apply(ctx, rec, expectedClaimer, expectedRev); err != nil {
		if errors.Is(err, ErrClaimConflict) {
			return nil, fmt.Errorf("%w: lost the race for %q", ErrLeaseHeld, key)
		}
		return nil, err
	}
	rec.Revision = expectedRev + 1
	return &SessionLease{svc: s, key: key, rec: rec}, nil
}

// Heartbeat extends a held lease. A turn that outruns the TTL without
// heartbeating is treated as dead by other nodes, so anything that can
// run longer than the TTL must call this.
//
// Returns ErrLeaseLost if the lease was taken over meanwhile, which
// the caller should treat as "stop": another node is now running this
// conversation.
func (l *SessionLease) Heartbeat(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return ErrLeaseLost
	}

	next := proto.Clone(l.rec).(*lobslawv1.SessionLease)
	next.ClaimExpiresAt = timestamppb.New(l.svc.now().Add(l.svc.ttl))

	if err := l.svc.apply(ctx, next, l.svc.nodeID, l.rec.Revision); err != nil {
		if errors.Is(err, ErrClaimConflict) {
			l.released = true
			return fmt.Errorf("%w: %q", ErrLeaseLost, l.key)
		}
		return err
	}
	next.Revision = l.rec.Revision + 1
	l.rec = next
	return nil
}

// Release drops the lease so the next turn can start immediately
// rather than waiting out the TTL. Idempotent.
//
// A failure here is logged, not returned: the turn has finished and
// its result is already written, and the lease expires on its own.
// Failing the turn over a lease we are about to stop caring about
// would turn a tidy-up problem into a user-visible one.
func (l *SessionLease) Release(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.released = true

	cleared := proto.Clone(l.rec).(*lobslawv1.SessionLease)
	cleared.ClaimedBy = ""
	cleared.ClaimExpiresAt = nil
	cleared.TurnId = ""

	if err := l.svc.apply(ctx, cleared, l.svc.nodeID, l.rec.Revision); err != nil {
		l.svc.log.Warn("session lease: release failed; it will expire on its own",
			"key", l.key, "ttl", l.svc.ttl, "err", err)
	}
}

// HeldBy reports the node holding the lease, for diagnostics.
func (l *SessionLease) HeldBy() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.GetClaimedBy()
}

func (s *LeaseService) load(key string) (*lobslawv1.SessionLease, error) {
	raw, err := s.store.Get(BucketSessionLeases, key)
	if err != nil {
		return nil, nil //nolint:nilerr // absent lease = unclaimed
	}
	var rec lobslawv1.SessionLease
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("session lease: decode %q: %w", key, err)
	}
	return &rec, nil
}

func (s *LeaseService) apply(ctx context.Context, rec *lobslawv1.SessionLease, expectedClaimer string, expectedRev uint64) error {
	entry := &lobslawv1.LogEntry{
		Op:               lobslawv1.LogOp_LOG_OP_CLAIM,
		Id:               rec.Id,
		Payload:          &lobslawv1.LogEntry_SessionLease{SessionLease: rec},
		ExpectedClaimer:  expectedClaimer,
		ExpectedRevision: &expectedRev,
	}
	data, err := proto.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session lease: marshal: %w", err)
	}
	// Forwarded when this node is not the leader, so a gateway on a
	// follower can still take a lease — which is the whole point,
	// since gateway placement is uncorrelated with raft leadership.
	resp, err := s.raft.ApplyOrForward(ctx, data, s.ttl)
	if err != nil {
		return err
	}
	// A rejected CAS is reported as the FSM's return VALUE, not as an
	// apply error — the entry replicated fine, it just did not pass
	// the condition. Missing this check reads every refused claim as a
	// success, which is precisely the failure the lease exists to
	// prevent. Forwarded writes surface it as an error from Propose
	// instead and return a nil response here.
	if ferr, ok := resp.(error); ok && ferr != nil {
		return ferr
	}
	return nil
}
