package node_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/node"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The queue's whole promise is that work handed to a bot gets done and
// the outcome is readable afterwards. This boots a real node, posts an
// item, and waits for the drain to work it — exercising the chain the
// unit tests each cover one link of: post → raft → FSM callback →
// scheduler wake → CAS claim → turn runner → resolve.
func TestPostedInboxItemIsDrainedAfterBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping inbox drain integration in short mode")
	}

	tmp := t.TempDir()
	nodeID := "inbox-boot-node"
	n, err := node.New(node.Config{
		NodeID: nodeID,
		// Compute as well as memory: the drain runs each item through
		// the turn runner, which a memory-only node does not have. On
		// such a node the scheduler finds no handler and releases the
		// claim for a sibling to take, which is correct and is why this
		// test has to ask for compute explicitly.
		Functions:      []types.NodeFunction{types.FunctionMemory, types.FunctionStorage, types.FunctionCompute},
		ListenAddr:     "127.0.0.1:0",
		DataDir:        filepath.Join(tmp, "data"),
		Bootstrap:      true,
		SnapshotTarget: "storage:test-backup",
		Creds:          signNodeCert(t, filepath.Join(tmp, "certs"), nodeID),
		MemoryKey:      mustKey(t),
		LLMProvider:    compute.NewMockProvider(compute.MockResponse{Content: "here is today's summary"}),
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	if n.Inbox() == nil {
		t.Fatal("Inbox() nil — the queue should be wired wherever raft is")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()

	waitForChief(ctx, t, n)

	item, err := n.Inbox().Post(ctx, &lobslawv1.BotInboxItem{
		Recipient: memory.ChiefBotID,
		Sender:    "user:alice",
		Body:      "summarise what happened today",
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	worked := waitForTerminal(ctx, t, n, memory.ChiefBotID, item.GetId())

	if worked.GetStatus() != lobslawv1.InboxStatus_INBOX_STATUS_DONE {
		t.Fatalf("status = %v (error %q), want done", worked.GetStatus(), worked.GetError())
	}
	// The result is the point of the queue: the outcome lives on the
	// item, so a bot that delegated work — and the GUI — can read what
	// happened without replaying a transcript.
	if !strings.Contains(worked.GetResult(), "today's summary") {
		t.Errorf("result = %q, want the turn's reply recorded on the item", worked.GetResult())
	}
	if worked.GetCompletedAt() == nil {
		t.Error("completed_at was not stamped")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned: %v", err)
	}
}

func waitForTerminal(ctx context.Context, t *testing.T, n *node.Node, recipient, id string) *lobslawv1.BotInboxItem {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last *lobslawv1.BotInboxItem
	for time.Now().Before(deadline) {
		item, err := n.Inbox().Get(ctx, recipient, id)
		if err == nil {
			last = item
			switch item.GetStatus() {
			case lobslawv1.InboxStatus_INBOX_STATUS_DONE,
				lobslawv1.InboxStatus_INBOX_STATUS_FAILED:
				return item
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil {
		t.Fatalf("item %q vanished from the queue", id)
	}
	t.Fatalf("item %q never finished: status %v after %d attempts", id, last.GetStatus(), last.GetAttempts())
	return nil
}
