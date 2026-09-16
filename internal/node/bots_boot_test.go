package node_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/node"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A booted raft node ends up with exactly one bot, it is the coordinator,
// and its personality overlay key is the constant every pre-existing
// cluster already has a record under.
//
// That last assertion is the upgrade story. If the coordinator ever gets a
// key of its own, every existing deployment wakes up after an upgrade
// with a default personality — a regression that is silent, lands on
// the user rather than the operator, and has no obvious connection to
// the change that caused it.
func TestNodeSeedsExactlyOneChiefBotAtBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bot seed integration in short mode")
	}

	tmp := t.TempDir()
	nodeID := "bots-boot-node"
	n, err := node.New(node.Config{
		NodeID:         nodeID,
		Functions:      []types.NodeFunction{types.FunctionMemory, types.FunctionStorage},
		ListenAddr:     "127.0.0.1:0",
		DataDir:        filepath.Join(tmp, "data"),
		Bootstrap:      true,
		SnapshotTarget: "storage:test-backup",
		Creds:          signNodeCert(t, filepath.Join(tmp, "certs"), nodeID),
		MemoryKey:      mustKey(t),
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	if n.Bots() == nil {
		t.Fatal("Bots() nil — the registry should be wired wherever raft is")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()

	coordinator := waitForCoordinator(ctx, t, n)

	if !coordinator.GetIsCoordinator() {
		t.Error("the seeded bot is not marked coordinator")
	}
	if !coordinator.GetEnabled() {
		t.Error("the seeded coordinator is disabled; nothing would answer an inbound message")
	}
	if got := memory.SoulTuneRecordIDFor(coordinator.GetId()); got != memory.SoulTuneRecordID {
		t.Errorf("coordinator overlay key = %q, want the pre-existing %q — an upgrade would lose the deployment's personality",
			got, memory.SoulTuneRecordID)
	}

	list, err := n.Bots().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("registry holds %d bots after boot, want exactly 1", len(list))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned: %v", err)
	}
}

func waitForCoordinator(ctx context.Context, t *testing.T, n *node.Node) *lobslawv1.BotRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rec, err := n.Bots().Get(ctx, memory.CoordinatorBotID); err == nil {
			return rec
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no coordinator bot was seeded within 15s of boot")
	return nil
}
