package gateway

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// A turn is Load history → run the agent (seconds, with tool loops) →
// Append. Nothing made that atomic, and in webhook mode every update
// arrives on its own net/http goroutine. Two messages during one turn
// both read the same prior history and both append, so the transcript
// interleaves and duplicates — and now that sessions are durable, the
// corruption is durable too rather than evaporating with the cache.
//
// Polling mode never had this: dispatchUpdate is called in a plain
// loop, so updates were already serialised there. The gate makes the
// two paths agree instead of leaving correctness dependent on which
// transport an operator chose.
//
// It also answers the ordinary human habit of sending three short
// messages in a row, which previously raced with itself.

// QueueMode decides what happens to a message that arrives while a
// turn is already running for the same session.
type QueueMode string

const (
	// QueueSerial queues behind the in-flight turn and runs in
	// arrival order. The safe default: nothing is dropped, nothing
	// interleaves.
	QueueSerial QueueMode = "serial"

	// QueueLatest keeps only the newest queued message and discards
	// the ones it overtook. For deployments where a stale question is
	// worse than a missing one.
	QueueLatest QueueMode = "latest"

	// QueueDebounce holds briefly and folds consecutive messages into
	// a single turn. Matches how people actually type — three
	// fragments become one turn with one reply, rather than three
	// turns racing to answer half a thought each.
	QueueDebounce QueueMode = "debounce"

	// QueueOff drops messages that arrive mid-turn. The caller is
	// expected to tell the user something is still running.
	QueueOff QueueMode = "off"
)

// DefaultDebounce is the fold window when debounce mode is on and the
// operator has not chosen one.
const DefaultDebounce = 3 * time.Second

// ParseQueueMode maps config text to a mode, defaulting to serial.
// Serial rather than the operator's likely intent, because the modes
// that drop messages should never be reached by a typo.
func ParseQueueMode(s string) QueueMode {
	switch QueueMode(strings.ToLower(strings.TrimSpace(s))) {
	case QueueLatest:
		return QueueLatest
	case QueueDebounce:
		return QueueDebounce
	case QueueOff:
		return QueueOff
	default:
		return QueueSerial
	}
}

// IsValid reports whether s names a real mode. Config validation uses
// this so a typo fails at boot rather than silently becoming serial.
func (m QueueMode) IsValid() bool {
	switch m {
	case QueueSerial, QueueLatest, QueueDebounce, QueueOff:
		return true
	}
	return false
}

// Disposition is what the gate decided about one inbound message.
type Disposition int

const (
	// Admitted means this caller owns the session and must run the
	// turn, then call Lease.Release.
	Admitted Disposition = iota

	// Folded means the message was merged into another caller's turn.
	// This caller must NOT run a turn and must not reply — the turn
	// that absorbed it answers for both.
	Folded

	// Dropped means the message was discarded: QueueOff during a
	// turn, or overtaken under QueueLatest. The caller should tell
	// the user, since nothing else will.
	Dropped
)

// Lease is ownership of a session for the duration of one turn.
type Lease struct {
	gate *TurnGate
	key  string

	// Batch is every message this turn is answering: the one that was
	// admitted, plus any folded into it while it waited. Callers must
	// use this rather than the message they arrived with, or a folded
	// fragment is silently ignored.
	Batch []string

	// Superseded counts messages dropped under QueueLatest to let
	// this turn run. Callers may mention it; the count exists so the
	// discard is at least observable.
	Superseded int
}

// Release hands the session to the next waiter. Safe to call once;
// further calls are no-ops so a deferred Release after an early
// return cannot double-release.
func (l *Lease) Release() {
	if l == nil || l.gate == nil {
		return
	}
	g := l.gate
	l.gate = nil
	g.release(l.key)
}

// TurnGate serialises turns per session.
//
// In-process only. Two nodes both serving the same session still
// race — that needs the raft-backed per-session lease (R3's cluster
// half). In practice one node serves a given session today: polling
// is leader-pinned by the singleton gate, and a webhook has exactly
// one endpoint.
type TurnGate struct {
	mode     QueueMode
	debounce time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	sessions map[string]*gateState
}

type gateState struct {
	running bool
	// waiters is the FIFO of turns blocked on this session. Serial
	// keeps them all; latest keeps at most one; debounce folds into
	// the head rather than appending.
	waiters []*waiter
}

type waiter struct {
	ready      chan Disposition
	batch      []string
	superseded int
}

// NewTurnGate builds a gate. A zero debounce with QueueDebounce takes
// DefaultDebounce; debounce is ignored in every other mode.
func NewTurnGate(mode QueueMode, debounce time.Duration, log *slog.Logger) *TurnGate {
	if !mode.IsValid() {
		mode = QueueSerial
	}
	if mode == QueueDebounce && debounce <= 0 {
		debounce = DefaultDebounce
	}
	if log == nil {
		log = slog.Default()
	}
	return &TurnGate{
		mode:     mode,
		debounce: debounce,
		log:      log,
		sessions: make(map[string]*gateState),
	}
}

// Acquire decides what happens to one inbound message and, when the
// answer is Admitted, blocks until this caller owns the session.
//
// text is the message body, carried so that debounce and latest can
// fold or replace it; the returned Lease.Batch is what the turn must
// actually answer.
//
// A cancelled ctx while queued yields Dropped: the user's client has
// gone, and running a turn to answer nobody costs tokens and may
// still write to the transcript.
func (g *TurnGate) Acquire(ctx context.Context, key, text string) (*Lease, Disposition) {
	g.mu.Lock()

	st := g.sessions[key]
	if st == nil {
		st = &gateState{}
		g.sessions[key] = st
	}

	if !st.running {
		st.running = true
		g.mu.Unlock()
		// Debounce applies to an idle session too, or the first
		// fragment of a burst always starts its own turn and only the
		// rest fold — which is the case the mode exists to prevent.
		if g.mode == QueueDebounce {
			return g.foldWindow(ctx, key, text), Admitted
		}
		return &Lease{gate: g, key: key, Batch: []string{text}}, Admitted
	}

	switch g.mode {
	case QueueOff:
		g.mu.Unlock()
		return nil, Dropped

	case QueueDebounce:
		// Fold into whoever is already waiting; only start a new
		// waiter if nobody is.
		if len(st.waiters) > 0 {
			w := st.waiters[0]
			w.batch = append(w.batch, text)
			g.mu.Unlock()
			return nil, Folded
		}

	case QueueLatest:
		// Overtake anyone queued. They have not run, so discarding
		// them here is the whole point of the mode.
		for _, w := range st.waiters {
			w.ready <- Dropped
		}
		dropped := len(st.waiters)
		st.waiters = nil
		w := &waiter{ready: make(chan Disposition, 1), batch: []string{text}, superseded: dropped}
		st.waiters = append(st.waiters, w)
		g.mu.Unlock()
		return g.wait(ctx, key, w)
	}

	w := &waiter{ready: make(chan Disposition, 1), batch: []string{text}}
	st.waiters = append(st.waiters, w)
	g.mu.Unlock()
	return g.wait(ctx, key, w)
}

// wait blocks until this waiter is handed the session or gives up.
func (g *TurnGate) wait(ctx context.Context, key string, w *waiter) (*Lease, Disposition) {
	select {
	case d := <-w.ready:
		if d != Admitted {
			return nil, d
		}
		// Handed the session, but the caller may have given up while
		// we were being handed it. Returning Admitted here would be
		// correct only if every caller released even on a context it
		// had already abandoned — and the obvious way to write such a
		// caller ("ctx dead? return") leaks the session forever.
		// Admitted therefore means the context was still live at
		// hand-off.
		if err := ctx.Err(); err != nil {
			g.release(key)
			return nil, Dropped
		}
		return &Lease{gate: g, key: key, Batch: w.batch, Superseded: w.superseded}, Admitted

	case <-ctx.Done():
		g.mu.Lock()
		found := false
		if st := g.sessions[key]; st != nil {
			for i, other := range st.waiters {
				if other == w {
					st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
					found = true
					break
				}
			}
		}
		g.mu.Unlock()
		if found {
			return nil, Dropped
		}

		// We were not in the queue, so something already took us out
		// of it and sent a verdict — release(), or a latest-mode
		// eviction, or a fold window. The send is buffered, so it
		// completed whether or not we were reading.
		//
		// If that verdict was Admitted we now own a session we are
		// about to abandon, and dropping it on the floor wedges the
		// conversation for good. Hand it on.
		if d := <-w.ready; d == Admitted {
			g.release(key)
		}
		return nil, Dropped
	}
}

// foldWindow holds an idle-session turn open for the debounce window
// so fragments typed straight after the first join the same turn.
func (g *TurnGate) foldWindow(ctx context.Context, key, text string) *Lease {
	timer := time.NewTimer(g.debounce)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		// Still return a lease: we hold the session and must release
		// it. The caller sees a cancelled ctx and can abandon.
	}

	// Absorb anything that queued during the window.
	g.mu.Lock()
	batch := []string{text}
	if st := g.sessions[key]; st != nil {
		for _, w := range st.waiters {
			batch = append(batch, w.batch...)
			w.ready <- Folded
		}
		st.waiters = nil
	}
	g.mu.Unlock()
	return &Lease{gate: g, key: key, Batch: batch}
}

// release hands the session to the next waiter, or marks it idle.
func (g *TurnGate) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.sessions[key]
	if st == nil {
		return
	}
	if len(st.waiters) == 0 {
		// Drop the entry so a long-lived process does not accumulate
		// one map slot per conversation it has ever seen.
		delete(g.sessions, key)
		return
	}

	next := st.waiters[0]
	st.waiters = st.waiters[1:]

	// Under debounce, everything still queued belongs to this turn:
	// they arrived while it was blocked, which is exactly the burst
	// the mode folds.
	if g.mode == QueueDebounce {
		for _, w := range st.waiters {
			next.batch = append(next.batch, w.batch...)
			w.ready <- Folded
		}
		st.waiters = nil
	}
	next.ready <- Admitted
}

// Mode reports the configured queue mode, so callers can tailor what
// they tell the user about a dropped message.
func (g *TurnGate) Mode() QueueMode { return g.mode }
