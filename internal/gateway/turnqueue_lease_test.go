package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The in-process queue and the cluster lease answer different
// questions, and both have to hold. These cover the second: whether
// this node may run the conversation at all.

type fakeLeaser struct {
	mu       sync.Mutex
	held     map[string]bool // keys another node is holding
	acquired []string
	turnIDs  []string
	handles  []*fakeHandle

	// beatErr/beatErrAt are stamped onto each handle at creation.
	// Configured before Acquire rather than mutated afterwards: the
	// heartbeat goroutine starts inside Acquire and reads them.
	beatErr   error
	beatErrAt int32
}

type fakeHandle struct {
	beats     atomic.Int32
	released  atomic.Bool
	beatErr   error // immutable once the handle is handed out
	beatErrAt int32
}

func (h *fakeHandle) Heartbeat(context.Context) error {
	n := h.beats.Add(1)
	if h.beatErr != nil && n >= h.beatErrAt {
		return h.beatErr
	}
	return nil
}

func (h *fakeHandle) Release(context.Context) { h.released.Store(true) }

func (f *fakeLeaser) AcquireLease(_ context.Context, key, turnID string) (LeaseHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held[key] {
		return nil, ErrLeaseUnavailable
	}
	h := &fakeHandle{beatErr: f.beatErr, beatErrAt: f.beatErrAt}
	f.handles = append(f.handles, h)
	f.acquired = append(f.acquired, key)
	f.turnIDs = append(f.turnIDs, turnID)
	return h, nil
}

func (f *fakeLeaser) hold(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held == nil {
		f.held = map[string]bool{}
	}
	f.held[key] = true
}

func (f *fakeLeaser) stats() (acquired, turnIDs []string, handles []*fakeHandle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acquired...), append([]string(nil), f.turnIDs...), append([]*fakeHandle(nil), f.handles...)
}

// Winning the local queue is not enough: another node may own the
// conversation, and running anyway is the cross-node version of the
// interleaving the gate exists to prevent.
func TestClusterLeaseHeldElsewhereBlocksTheTurn(t *testing.T) {
	t.Parallel()
	leaser := &fakeLeaser{}
	leaser.hold("telegram:-1")
	g := NewTurnGate(QueueSerial, 0, nil).WithLeaser(leaser, time.Hour)

	if _, d := g.Acquire(context.Background(), "telegram:-1", "turn", "hi"); d != Dropped {
		t.Errorf("got %v, want Dropped — another node holds this conversation", d)
	}

	// The local slot must be handed back, or the conversation is
	// wedged on this node too once the remote holder releases.
	if n := g.queueLen("telegram:-1"); n != 0 {
		t.Errorf("queue length %d after a refused cluster lease", n)
	}
	lease, d := g.Acquire(context.Background(), "telegram:-2", "turn", "hi")
	if d != Admitted {
		t.Fatalf("an unrelated conversation got %v", d)
	}
	lease.Release()
}

func TestClusterLeaseIsTakenAndReleasedAroundTheTurn(t *testing.T) {
	t.Parallel()
	leaser := &fakeLeaser{}
	g := NewTurnGate(QueueSerial, 0, nil).WithLeaser(leaser, time.Hour)

	lease, d := g.Acquire(context.Background(), "rest:s1", "turn-7", "hi")
	if d != Admitted {
		t.Fatalf("got %v, want Admitted", d)
	}
	acquired, turnIDs, handles := leaser.stats()
	if len(acquired) != 1 || acquired[0] != "rest:s1" {
		t.Fatalf("acquired = %v, want [rest:s1]", acquired)
	}
	if turnIDs[0] != "turn-7" {
		t.Errorf("turn id = %q, want turn-7 — an operator needs to know which turn holds a stuck conversation", turnIDs[0])
	}
	if handles[0].released.Load() {
		t.Error("lease released before the turn ran")
	}

	lease.Release()
	if !handles[0].released.Load() {
		t.Error("cluster lease not released; the next turn waits out the TTL for nothing")
	}
}

// A turn that outruns the TTL without heartbeating is treated as dead
// by peers, and the conversation is taken over while it is still
// running.
func TestClusterLeaseHeartbeatsWhileTheTurnRuns(t *testing.T) {
	t.Parallel()
	leaser := &fakeLeaser{}
	g := NewTurnGate(QueueSerial, 0, nil).WithLeaser(leaser, 10*time.Millisecond)

	lease, d := g.Acquire(context.Background(), "rest:s1", "turn", "hi")
	if d != Admitted {
		t.Fatalf("got %v, want Admitted", d)
	}
	_, _, handles := leaser.stats()

	waitUntil(t, func() bool { return handles[0].beats.Load() >= 3 }, "the lease was never heartbeated")

	lease.Release()
	settled := handles[0].beats.Load()
	time.Sleep(50 * time.Millisecond)
	if got := handles[0].beats.Load(); got > settled+1 {
		t.Errorf("heartbeats continued after release: %d then %d", settled, got)
	}
}

// Losing the lease mid-turn must stop the heartbeat rather than spin
// against a conversation another node now owns.
func TestHeartbeatStopsWhenTheLeaseIsLost(t *testing.T) {
	t.Parallel()
	// Fail from the second beat on, configured up front.
	leaser := &fakeLeaser{beatErr: errors.New("lease lost"), beatErrAt: 2}
	g := NewTurnGate(QueueSerial, 0, nil).WithLeaser(leaser, 5*time.Millisecond)

	lease, d := g.Acquire(context.Background(), "rest:s1", "turn", "hi")
	if d != Admitted {
		t.Fatalf("got %v, want Admitted", d)
	}
	_, _, handles := leaser.stats()

	waitUntil(t, func() bool { return handles[0].beats.Load() >= 2 }, "never beat twice")
	settled := handles[0].beats.Load()
	time.Sleep(60 * time.Millisecond)
	if got := handles[0].beats.Load(); got > settled+1 {
		t.Errorf("heartbeat kept going after losing the lease: %d then %d", settled, got)
	}
	lease.Release()
}

// A gate with no leaser is the single-node case and must behave
// exactly as before — this is what every existing test relies on.
func TestNoLeaserLeavesTheGateInProcessOnly(t *testing.T) {
	t.Parallel()
	g := NewTurnGate(QueueSerial, 0, nil)
	lease, d := g.Acquire(context.Background(), "rest:s1", "turn", "hi")
	if d != Admitted {
		t.Fatalf("got %v, want Admitted", d)
	}
	if lease.cluster != nil {
		t.Error("a gate with no leaser should hold no cluster lease")
	}
	lease.Release()
}
