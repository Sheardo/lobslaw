package trace

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// The recorder, and the one property that governs its whole design:
// TRACING MUST NEVER SLOW OR FAIL A TURN.
//
// A collector that hangs, a disk that fills, a sink that errors — none
// of it may reach the user waiting for a reply. So Record never
// blocks: it hands the span to a buffered channel and, if that channel
// is full, DROPS IT AND COUNTS THE DROP.
//
// Dropping is the correct behaviour and the count is what makes it
// honest. A trace with a hole in it that says "4 spans dropped" is
// usable; one that silently omits the interesting span is worse than
// no trace, because it is read as evidence of absence.
//
// This is also why traces are kept away from the hash-chained audit
// log. An audit entry that may be dropped under load is not an audit
// entry, and a trace that must never be dropped becomes a reliability
// problem on the reply path.

// Sink receives completed spans. Implementations must tolerate being
// called from a single background goroutine and must not panic.
type Sink interface {
	// Write handles one span. An error is logged and counted; it never
	// propagates to the turn.
	Write(Span) error
	// Close flushes. Called once at shutdown.
	Close() error
}

// DefaultBuffer is how many spans may queue before dropping starts.
//
// A few hundred: enough to absorb a burst from one busy turn without
// letting a stalled sink accumulate unbounded memory. The failure mode
// this bounds is a sink that has stopped consuming, and in that case
// the right amount of memory to spend is "some, briefly".
const DefaultBuffer = 256

// Recorder fans completed spans out to sinks, off the turn path.
type Recorder struct {
	spans chan Span
	sinks []Sink
	log   *slog.Logger

	dropped atomic.Uint64
	written atomic.Uint64
	failed  atomic.Uint64

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// NewRecorder starts the background writer.
//
// Returns nil when no sinks are configured, and nil is a usable
// *Recorder: every method tolerates it. That is the off-by-default
// path — absence rather than a flag each call site has to remember to
// check.
func NewRecorder(log *slog.Logger, sinks ...Sink) *Recorder {
	live := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Recorder{
		spans: make(chan Span, DefaultBuffer),
		sinks: live,
		log:   log,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go r.run()
	return r
}

// Record queues a span. Never blocks, never errors, safe on nil.
func (r *Recorder) Record(s Span) {
	if r == nil {
		return
	}
	select {
	case r.spans <- s:
	default:
		// The turn is more important than the record of it.
		r.dropped.Add(1)
	}
}

// Stats reports what the recorder has done. Read by the CLI and by the
// shutdown log, so a deployment can tell a quiet trace from a dropped
// one.
type Stats struct {
	Written uint64
	Dropped uint64
	Failed  uint64
}

// Stats snapshots the counters. Safe on nil.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{
		Written: r.written.Load(),
		Dropped: r.dropped.Load(),
		Failed:  r.failed.Load(),
	}
}

func (r *Recorder) run() {
	defer close(r.done)
	for {
		select {
		case s := <-r.spans:
			r.dispatch(s)
		case <-r.stop:
			// Drain what is already queued. A shutdown that discards
			// the buffer loses precisely the spans from the turn that
			// was in flight when the operator hit stop, which is
			// usually the one they wanted.
			for {
				select {
				case s := <-r.spans:
					r.dispatch(s)
				default:
					return
				}
			}
		}
	}
}

func (r *Recorder) dispatch(s Span) {
	for _, sink := range r.sinks {
		if err := sink.Write(s); err != nil {
			r.failed.Add(1)
			// Debug, not warn. A sink erroring on every span would
			// otherwise produce one warning per span and drown the log
			// it is meant to complement; the count in Stats is the
			// signal, and Close reports it once at a level people read.
			r.log.Debug("trace: sink write failed", "err", err)
			continue
		}
		r.written.Add(1)
	}
}

// Close stops the writer, drains the queue and closes every sink.
// Idempotent and safe on nil.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() {
		close(r.stop)
		<-r.done
		stats := r.Stats()
		// Said once, at a level somebody reads. A deployment whose
		// traces are half missing should learn it here rather than by
		// noticing gaps months later.
		if stats.Dropped > 0 || stats.Failed > 0 {
			r.log.Warn("trace: spans were lost",
				"written", stats.Written, "dropped", stats.Dropped, "failed", stats.Failed)
		} else {
			r.log.Info("trace: recorder stopped", "written", stats.Written)
		}
		for _, s := range r.sinks {
			if err := s.Close(); err != nil {
				r.log.Warn("trace: sink close failed", "err", err)
			}
		}
	})
	return nil
}

// contextKey is unexported so nothing outside can collide with it.
type contextKey struct{}

// turnTrace is what travels on the context: the recorder and the turn
// id together, because a span without a turn id cannot be grouped and
// a caller that has to remember to pass it separately will eventually
// not.
type turnTrace struct {
	rec    *Recorder
	turnID string
}

// WithTurn attaches a recorder and turn id to a context, so code deep
// in a call stack can emit a span without every intermediate signature
// growing a parameter.
func WithTurn(ctx context.Context, r *Recorder, turnID string) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, turnTrace{rec: r, turnID: turnID})
}

// FromContext returns the recorder and turn id, if tracing is on.
//
// Returns a nil recorder when it is not, and a nil recorder is usable —
// so the caller writes trace.FromContext(ctx) and records
// unconditionally, rather than branching on whether tracing exists.
func FromContext(ctx context.Context) (*Recorder, string) {
	t, ok := ctx.Value(contextKey{}).(turnTrace)
	if !ok {
		return nil, ""
	}
	return t.rec, t.turnID
}
