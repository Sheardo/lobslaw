package memory

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/jmylchreest/lobslaw/pkg/crypto"
)

// Two gateways can reach the same conversation at once — a webhook and
// a REST client, or two nodes behind a load balancer. The in-process
// TurnGate cannot see across that boundary; these tests are about the
// part that can.

// newLeaseStack returns a raft-backed store plus a lease-service
// factory, so a test can mint several services that look like
// separate nodes contending on one cluster.
func newLeaseStack(t *testing.T) (func(nodeID string, now func() time.Time) *LeaseService, *Store) {
	t.Helper()
	dataDir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(dataDir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	fsm := NewFSM(store)
	_, inmem := raft.NewInmemTransport("lease-node")
	node, err := NewRaft(RaftConfig{
		NodeID: "lease-node", LocalAddr: "lease-node",
		DataDir: dataDir, Bootstrap: true, Transport: inmem,
	}, fsm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Shutdown()
		_ = store.Close()
	})
	if err := node.WaitForLeader(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	return func(nodeID string, now func() time.Time) *LeaseService {
		return NewLeaseService(node, store, LeaseConfig{
			NodeID: nodeID, TTL: time.Minute, Now: now,
		})
	}, store
}

func TestLeaseExcludesASecondNode(t *testing.T) {
	t.Parallel()
	newSvc, _ := newLeaseStack(t)
	a := newSvc("node-a", nil)
	b := newSvc("node-b", nil)

	held, err := a.Acquire(context.Background(), "telegram:-100", "turn-1")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if held == nil {
		t.Fatal("no lease returned")
	}

	if _, err := b.Acquire(context.Background(), "telegram:-100", "turn-2"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second node got %v, want ErrLeaseHeld — both would run a turn on one conversation", err)
	}

	// A different conversation is unaffected: one busy chat must not
	// stall the rest of the deployment.
	other, err := b.Acquire(context.Background(), "telegram:-200", "turn-3")
	if err != nil {
		t.Fatalf("unrelated conversation was blocked: %v", err)
	}
	other.Release(context.Background())
}

func TestLeaseReleaseLetsTheNextTurnStart(t *testing.T) {
	t.Parallel()
	newSvc, _ := newLeaseStack(t)
	a := newSvc("node-a", nil)
	b := newSvc("node-b", nil)

	held, err := a.Acquire(context.Background(), "rest:s1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	held.Release(context.Background())

	got, err := b.Acquire(context.Background(), "rest:s1", "turn-2")
	if err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	if got.HeldBy() != "node-b" {
		t.Errorf("HeldBy = %q, want node-b", got.HeldBy())
	}

	// Release is idempotent — a deferred Release after an explicit one
	// is the obvious way to write a caller.
	got.Release(context.Background())
	got.Release(context.Background())
}

// A node that dies mid-turn must not lock the conversation forever.
func TestLeaseExpiryAllowsTakeover(t *testing.T) {
	t.Parallel()
	newSvc, _ := newLeaseStack(t)

	clock := time.Now()
	frozen := func() time.Time { return clock }
	a := newSvc("node-a", frozen)
	b := newSvc("node-b", frozen)

	if _, err := a.Acquire(context.Background(), "telegram:-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	// Still inside the TTL: nobody else gets in.
	if _, err := b.Acquire(context.Background(), "telegram:-1", "turn-2"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("got %v, want ErrLeaseHeld while the lease is live", err)
	}

	// node-a is now presumed dead.
	clock = clock.Add(2 * time.Minute)
	took, err := b.Acquire(context.Background(), "telegram:-1", "turn-3")
	if err != nil {
		t.Fatalf("expired lease was not taken over: %v — the conversation would stay locked to a dead node", err)
	}
	if took.HeldBy() != "node-b" {
		t.Errorf("HeldBy = %q, want node-b", took.HeldBy())
	}
}

// A long turn keeps its lease by heartbeating; one that stops
// heartbeating and gets taken over must find out, so it can stop
// rather than write on top of the new holder.
func TestLeaseHeartbeatExtendsAndDetectsTakeover(t *testing.T) {
	t.Parallel()
	newSvc, _ := newLeaseStack(t)

	clock := time.Now()
	frozen := func() time.Time { return clock }
	a := newSvc("node-a", frozen)
	b := newSvc("node-b", frozen)

	held, err := a.Acquire(context.Background(), "telegram:-9", "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	// Past the original expiry, but heartbeated in time.
	clock = clock.Add(50 * time.Second)
	if err := held.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}
	clock = clock.Add(50 * time.Second)
	if _, err := b.Acquire(context.Background(), "telegram:-9", "turn-2"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("got %v, want ErrLeaseHeld — the heartbeat should have kept the lease alive", err)
	}

	// Now node-a stalls and node-b takes over.
	clock = clock.Add(2 * time.Minute)
	if _, err := b.Acquire(context.Background(), "telegram:-9", "turn-3"); err != nil {
		t.Fatalf("takeover after a missed heartbeat failed: %v", err)
	}
	if err := held.Heartbeat(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale holder's heartbeat returned %v, want ErrLeaseLost", err)
	}
}

// The same node re-entering a conversation it already holds is the
// restart case: its own stale lease must not lock it out.
func TestLeaseSameNodeReacquires(t *testing.T) {
	t.Parallel()
	newSvc, _ := newLeaseStack(t)
	a := newSvc("node-a", nil)

	if _, err := a.Acquire(context.Background(), "rest:s", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(context.Background(), "rest:s", "turn-2"); err != nil {
		t.Fatalf("a node was locked out by its own lease: %v", err)
	}
}

// Without raft there is no second node to race with, and refusing
// would take a single-node gateway offline over a hazard it cannot
// have.
func TestLeaseWithoutRaftIsANoOp(t *testing.T) {
	t.Parallel()
	svc := NewLeaseService(nil, nil, LeaseConfig{NodeID: "solo"})
	lease, err := svc.Acquire(context.Background(), "telegram:-1", "turn-1")
	if err != nil {
		t.Fatalf("no-raft acquire failed: %v", err)
	}
	if lease != nil {
		t.Error("expected a nil lease when there is nothing to claim against")
	}
	// The nil lease must be safe to use, so callers need no branch.
	lease.Release(context.Background())
	if err := lease.Heartbeat(context.Background()); err != nil {
		t.Errorf("heartbeat on a nil lease: %v", err)
	}
	if lease.HeldBy() != "" {
		t.Error("nil lease should report no holder")
	}
}
