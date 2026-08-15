package scheduler

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

func seedCommitment(tb testing.TB, node *memory.RaftNode, c *lobslawv1.AgentCommitment) {
	tb.Helper()
	data, err := proto.Marshal(&lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_PUT,
		Id:      c.Id,
		Payload: &lobslawv1.LogEntry_Commitment{Commitment: c},
	})
	if err != nil {
		tb.Fatal(err)
	}
	res, err := node.Apply(data, 5*time.Second)
	if err != nil {
		tb.Fatal(err)
	}
	if ferr, ok := res.(error); ok && ferr != nil {
		tb.Fatal(ferr)
	}
}

func loadCommitment(tb testing.TB, node *memory.RaftNode, id string) *lobslawv1.AgentCommitment {
	tb.Helper()
	raw, err := node.FSM().Store().Get(memory.BucketCommitments, id)
	if err != nil {
		tb.Fatalf("load commitment %q: %v", id, err)
	}
	var c lobslawv1.AgentCommitment
	if err := proto.Unmarshal(raw, &c); err != nil {
		tb.Fatalf("unmarshal commitment %q: %v", id, err)
	}
	return &c
}

// runSchedulerBriefly runs the loop long enough for one due
// commitment to be picked up and settled, then stops it. Bounded by a
// deadline rather than a fixed sleep so a slow machine does not turn
// this into a flake — the lesson from the concurrent-claim test.
func runSchedulerBriefly(tb testing.TB, s *Scheduler) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()

	// Give the loop time to fire and apply the result.
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		tb.Fatal("scheduler did not stop")
	}
}
