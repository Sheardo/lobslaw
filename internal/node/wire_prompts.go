package node

import (
	"context"
	"fmt"

	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/singleton"
)

// promptSweeperName is the singleton key for the expiry sweeper.
const promptSweeperName = "prompt-sweeper"

// wirePrompts picks the confirmation registry this node can actually
// back, and starts the expiry sweeper if it hosts raft.
//
// A gateway on a compute-only node has no local raft, so it keeps the
// in-memory registry: same behaviour as before, confined to one
// process. Anywhere raft is present, a confirmation issued here can be
// answered on a peer and survives this process restarting.
func (n *Node) wirePrompts() error {
	if n.raft == nil || n.store == nil {
		n.log.Info("prompts: no local raft; confirmations are process-local",
			"reason", "gateway on a node without the memory function")
		n.promptRegistry = gateway.NewPromptRegistry()
		return nil
	}

	store, err := memory.NewPromptStore(memory.PromptStoreConfig{
		Raft:  n.raft,
		Store: n.store,
		Log:   n.log,
	})
	if err != nil {
		return fmt.Errorf("prompt store: %w", err)
	}
	n.promptRegistry = gateway.NewRaftPrompts(store, n.cfg.NodeID)
	n.promptStore = store
	return nil
}

// startPromptSweeper closes out expired confirmations on whichever
// node holds leadership. Leader-pinned rather than per-node because
// every node sweeping would be correct but would burn a raft
// round-trip per node per expiry.
func (n *Node) startPromptSweeper(ctx context.Context) {
	if n.promptStore == nil || n.leaderGate == nil {
		return
	}
	go func() {
		err := singleton.Run(ctx, n.leaderGate, promptSweeperName, n.log,
			func(ctx context.Context) error {
				return n.promptStore.SweepLoop(ctx, memory.DefaultSweepInterval)
			})
		if err != nil && ctx.Err() == nil {
			n.log.Warn("prompt sweeper stopped", "err", err)
		}
	}()
}
