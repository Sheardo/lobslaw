package node

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
)

// Pinned memory sits in the part of the prompt a provider caches.
// Reading it fresh each turn would mean every write invalidated the
// prefix for the turn after it — always-on and never cached, the worst
// of both. So the rendered form is frozen per session: writes are
// durable immediately, the snapshot refreshes at the next boundary.

func pinnedNode(t *testing.T) (*Node, *memory.PinnedStore) {
	t.Helper()
	dir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(dir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	_, inmem := raft.NewInmemTransport("pinned-node")
	node, err := memory.NewRaft(memory.RaftConfig{
		NodeID: "pinned-node", LocalAddr: "pinned-node",
		DataDir: dir, Bootstrap: true, Transport: inmem,
	}, memory.NewFSM(store))
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
	ps, err := memory.NewPinnedStore(node, store, memory.PinnedConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return &Node{pinnedStore: ps, log: slog.New(slog.DiscardHandler)}, ps
}

// The acceptance criterion: the prefix is byte-identical across turns
// within a session, even when a write lands in between.
func TestSnapshotIsFrozenForTheSession(t *testing.T) {
	t.Parallel()
	n, store := pinnedNode(t)
	provider := n.pinnedProvider()
	ctx := t.Context()

	if err := store.Add(ctx, memory.PinnedProfile, "alice", "prefers terse replies"); err != nil {
		t.Fatal(err)
	}

	first := provider("telegram:1", "alice")
	if len(first.Profile) != 1 {
		t.Fatalf("profile = %v", first.Profile)
	}

	// A mid-session write: durable immediately...
	if err := store.Add(ctx, memory.PinnedProfile, "alice", "works in London"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.Get(memory.PinnedProfile, "alice"); len(rec.Entries) != 2 {
		t.Fatalf("the write did not land: %v", rec.Entries)
	}

	// ...but invisible to this session's prompt, which is the point.
	second := provider("telegram:1", "alice")
	if len(second.Profile) != 1 {
		t.Errorf("profile = %v; a mid-session write changed the prompt prefix and cost the cache",
			second.Profile)
	}
}

// A different conversation is a different prefix, so it sees the
// current state rather than the first one's frozen view.
func TestANewSessionSeesTheCurrentState(t *testing.T) {
	t.Parallel()
	n, store := pinnedNode(t)
	provider := n.pinnedProvider()
	ctx := t.Context()

	_ = store.Add(ctx, memory.PinnedProfile, "alice", "one")
	_ = provider("telegram:1", "alice")
	_ = store.Add(ctx, memory.PinnedProfile, "alice", "two")

	fresh := provider("telegram:2", "alice")
	if len(fresh.Profile) != 2 {
		t.Errorf("a new session got a stale snapshot: %v", fresh.Profile)
	}
}

// Two people in one conversation get their own blocks. A shared
// snapshot would show one participant's profile to everybody.
func TestSnapshotIsPerUserWithinAConversation(t *testing.T) {
	t.Parallel()
	n, store := pinnedNode(t)
	provider := n.pinnedProvider()
	ctx := t.Context()

	_ = store.Add(ctx, memory.PinnedProfile, "alice", "alice fact")
	_ = store.Add(ctx, memory.PinnedProfile, "bob", "bob fact")

	a := provider("telegram:1", "alice")
	b := provider("telegram:1", "bob")
	if len(a.Profile) != 1 || a.Profile[0] != "alice fact" {
		t.Errorf("alice = %v", a.Profile)
	}
	if len(b.Profile) != 1 || b.Profile[0] != "bob fact" {
		t.Errorf("bob = %v; a shared snapshot leaked between participants", b.Profile)
	}
}

// A turn with no principal renders no profile. Rendering somebody
// else's would be worse than rendering none.
func TestAnonymousTurnGetsNothing(t *testing.T) {
	t.Parallel()
	n, store := pinnedNode(t)
	provider := n.pinnedProvider()
	_ = store.Add(t.Context(), memory.PinnedProfile, "alice", "a fact")

	blocks := provider("telegram:1", "")
	if len(blocks.Profile) != 0 || len(blocks.Notes) != 0 {
		t.Errorf("an anonymous turn was handed somebody's memory: %+v", blocks)
	}
}

// A node without raft has nowhere to keep these, and must render the
// prompt exactly as it did before they existed.
func TestNoStoreYieldsNoProvider(t *testing.T) {
	t.Parallel()
	n := &Node{log: slog.New(slog.DiscardHandler)}
	if n.pinnedProvider() != nil {
		t.Error("a node with no pinned store returned a provider")
	}
}
