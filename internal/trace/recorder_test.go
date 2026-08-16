package trace

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// The property that governs the whole design: TRACING MUST NEVER SLOW
// OR FAIL A TURN. A collector that hangs, a disk that fills, a sink
// that errors — none of it may reach the user waiting for a reply.

type blockingSink struct {
	release chan struct{}
	mu      sync.Mutex
	got     []Span
}

func newBlockingSink() *blockingSink {
	return &blockingSink{release: make(chan struct{})}
}

func (b *blockingSink) Write(s Span) error {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, s)
	return nil
}
func (b *blockingSink) Close() error { return nil }

type countingSink struct {
	mu   sync.Mutex
	got  []Span
	err  error
	done chan struct{}
	want int
}

func newCountingSink(want int) *countingSink {
	return &countingSink{done: make(chan struct{}), want: want}
}

func (c *countingSink) Write(s Span) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.got = append(c.got, s)
	if len(c.got) == c.want {
		close(c.done)
	}
	return nil
}
func (c *countingSink) Close() error { return nil }

func (c *countingSink) spans() []Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Span(nil), c.got...)
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A sink that never returns must not stall the caller, and the spans
// it cannot take must be counted rather than silently vanish.
func TestAStalledSinkNeitherBlocksNorHidesTheLoss(t *testing.T) {
	t.Parallel()
	sink := newBlockingSink()
	r := NewRecorder(quiet(), sink)
	t.Cleanup(func() { close(sink.release) })

	// Far more than the buffer, from the "turn" goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range DefaultBuffer * 4 {
			r.Record(Span{TurnID: "t1", Kind: KindLLMCall})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a stalled sink")
	}

	if got := r.Stats().Dropped; got == 0 {
		t.Error("spans were lost with no drop recorded — a trace with a silent hole " +
			"is read as evidence of absence")
	}
}

// A trace with a hole that says "4 dropped" is usable; one that
// silently omits the interesting span is worse than no trace.
func TestDroppedSpansAreCounted(t *testing.T) {
	t.Parallel()
	sink := newBlockingSink()
	r := NewRecorder(quiet(), sink)
	t.Cleanup(func() { close(sink.release) })

	const sent = DefaultBuffer * 3
	for range sent {
		r.Record(Span{TurnID: "t1"})
	}
	stats := r.Stats()
	if stats.Dropped+stats.Written == 0 {
		t.Fatal("nothing was accounted for")
	}
	if stats.Dropped > uint64(sent) {
		t.Errorf("dropped %d of %d sent", stats.Dropped, sent)
	}
}

// A sink erroring on every span must not take the recorder down or
// stop the other sinks.
func TestAFailingSinkIsCountedAndDoesNotStopTheRest(t *testing.T) {
	t.Parallel()
	bad := newCountingSink(0)
	bad.err = errors.New("disk full")
	good := newCountingSink(1)
	r := NewRecorder(quiet(), bad, good)
	t.Cleanup(func() { _ = r.Close() })

	r.Record(Span{TurnID: "t1", Kind: KindLLMCall})
	select {
	case <-good.done:
	case <-time.After(2 * time.Second):
		t.Fatal("a failing sink stopped a healthy one")
	}
	if r.Stats().Failed == 0 {
		t.Error("the failure was not counted")
	}
}

// Off by default is absence, not a flag every call site remembers to
// check. A nil recorder has to be usable.
func TestNoSinksMeansNoRecorderAndNilIsSafe(t *testing.T) {
	t.Parallel()
	if r := NewRecorder(quiet()); r != nil {
		t.Fatal("a recorder was built with no sinks")
	}
	if r := NewRecorder(quiet(), nil, nil); r != nil {
		t.Fatal("nil sinks built a recorder")
	}

	var r *Recorder
	r.Record(Span{TurnID: "t1"}) // must not panic
	if got := r.Stats(); got != (Stats{}) {
		t.Errorf("stats = %+v", got)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}

// A shutdown that discards the buffer loses precisely the spans from
// the turn that was in flight, which is usually the one wanted.
func TestCloseDrainsWhatIsQueued(t *testing.T) {
	t.Parallel()
	sink := newCountingSink(10)
	r := NewRecorder(quiet(), sink)
	for i := range 10 {
		r.Record(Span{TurnID: "t1", Attempt: i})
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.spans()); got != 10 {
		t.Errorf("drained %d of 10", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	r := NewRecorder(quiet(), newCountingSink(0))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// --- context ------------------------------------------------------

func TestTheRecorderTravelsOnTheContext(t *testing.T) {
	t.Parallel()
	sink := newCountingSink(1)
	r := NewRecorder(quiet(), sink)
	t.Cleanup(func() { _ = r.Close() })

	ctx := WithTurn(context.Background(), r, "turn-42")
	got, turnID := FromContext(ctx)
	if got != r || turnID != "turn-42" {
		t.Fatalf("FromContext = %v, %q", got, turnID)
	}
}

// Tracing off means FromContext hands back a nil recorder that is
// still safe to call, so the caller records unconditionally rather
// than branching on whether tracing exists.
func TestAContextWithoutTracingIsStillUsable(t *testing.T) {
	t.Parallel()
	r, turnID := FromContext(context.Background())
	if r != nil || turnID != "" {
		t.Fatalf("got %v, %q", r, turnID)
	}
	r.Record(Span{TurnID: "t1"}) // must not panic
}

// WithTurn on a nil recorder returns the context unchanged rather than
// storing a nil, so a later FromContext cannot hand back a turn id
// with nothing to record against.
func TestWithTurnOnANilRecorderIsANoOp(t *testing.T) {
	t.Parallel()
	ctx := WithTurn(context.Background(), nil, "turn-42")
	if _, turnID := FromContext(ctx); turnID != "" {
		t.Errorf("turn id %q survived with no recorder", turnID)
	}
}
