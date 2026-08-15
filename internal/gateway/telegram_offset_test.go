package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The Telegram offset is a consumer position, and where it is written
// decides what a restart replays. Persisting it once per batch means a
// crash partway through leaves it covering none of the batch — so on
// restart every update in it is redelivered, and the dedup map is
// in-memory, so the ones that already ran run again. Duplicate
// replies, duplicate tool calls, duplicate commitments.

// recordingChannelState is a ChannelStateStore that remembers every
// offset written, in order.
type recordingChannelState struct {
	mu      sync.Mutex
	writes  []int64
	current []byte
}

func (c *recordingChannelState) Get(context.Context, string, string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return nil, types.ErrNotFound
	}
	return c.current, nil
}

func (c *recordingChannelState) Put(_ context.Context, _, _ string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = append([]byte(nil), value...)
	n, _ := strconv.ParseInt(string(value), 10, 64)
	c.writes = append(c.writes, n)
	return nil
}

func (c *recordingChannelState) acked() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.writes...)
}

// gatedProvider lets a test hold one turn open while the rest run.
type gatedProvider struct {
	mu      sync.Mutex
	seen    []string
	hangOn  string
	release chan struct{}
}

func (p *gatedProvider) Chat(ctx context.Context, req compute.ChatRequest) (*compute.ChatResponse, error) {
	var last string
	for _, m := range req.Messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	p.mu.Lock()
	p.seen = append(p.seen, last)
	hang := last == p.hangOn
	p.mu.Unlock()

	if hang {
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return &compute.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (p *gatedProvider) turns() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// A batch interrupted partway must leave the completed prefix
// acknowledged, so a restart redelivers only what did not finish.
func TestPollAcknowledgesEachUpdateAsItCompletes(t *testing.T) {
	t.Parallel()
	prov := &gatedProvider{hangOn: "second", release: make(chan struct{})}
	state := &recordingChannelState{}
	state.seed(100)
	h := newPollHarnessWithState(t, agentWithProvider(t, prov), [][]byte{
		batchOf(update(101, "first"), update(102, "second"), update(103, "third")),
	}, state)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = h.handler.pollLoop(ctx); close(done) }()

	// Wait until the first turn is acknowledged and the second is
	// stuck — this is the "crash mid-batch" moment.
	waitUntil(t, func() bool {
		acked := state.acked()
		return len(acked) >= 1 && acked[0] == 102
	}, "the first update was never acknowledged on its own")

	// Nothing past the in-flight update may be acknowledged.
	for _, a := range state.acked() {
		if a > 102 {
			t.Fatalf("acknowledged offset %d while update 102 was still running — a crash here would lose it", a)
		}
	}

	cancel()
	close(prov.release)
	<-done

	// The third never ran, so it must remain unacknowledged and be
	// redelivered on restart.
	if turns := prov.turns(); len(turns) > 2 {
		t.Errorf("provider saw %v; the third update should not have run after cancellation", turns)
	}
}

// The whole point: the acknowledged offset advances one update at a
// time, so a restart replays at most the turn that was in flight.
func TestPollOffsetAdvancesPerUpdate(t *testing.T) {
	t.Parallel()
	prov := &gatedProvider{release: make(chan struct{})}
	state := &recordingChannelState{}
	state.seed(6)
	h := newPollHarnessWithState(t, agentWithProvider(t, prov), [][]byte{
		batchOf(update(7, "a"), update(8, "b"), update(9, "c")),
	}, state)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = h.handler.pollLoop(ctx); close(done) }()

	waitUntil(t, func() bool {
		acked := state.acked()
		return len(acked) >= 3 && acked[2] == 10
	}, fmt.Sprintf("offsets were not acknowledged one at a time: %v", state.acked()))

	got := state.acked()[:3]
	for i, want := range []int64{8, 9, 10} {
		if got[i] != want {
			t.Errorf("ack %d = %d, want %d (offsets: %v)", i, got[i], want, got)
		}
	}
	cancel()
	<-done
}

// seed pre-populates the persisted offset. Without it the poll loop
// treats a store-backed run with no offset as a first run and
// discards the batch, which is correct behaviour and wrong for these
// tests.
func (c *recordingChannelState) seed(offset int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = []byte(strconv.FormatInt(offset, 10))
}

func update(id int64, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":5,"type":"private"},"from":{"id":42,"username":"alice"},"text":%q}}`,
		id, id, text)
}

func batchOf(updates ...string) []byte {
	return []byte("[" + strings.Join(updates, ",") + "]")
}
