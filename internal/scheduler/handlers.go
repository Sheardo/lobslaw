package scheduler

import (
	"context"
	"fmt"
	"sync"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// TaskHandler runs a scheduled task. Returning an error logs but
// doesn't block future firings. Handlers are expected to be idempotent
// — in a partition the same firing MAY dispatch twice (see the
// lobslaw-cluster-claim decision's partition caveat).
type TaskHandler func(ctx context.Context, task *lobslawv1.ScheduledTaskRecord) error

// CommitmentHandler runs a one-shot commitment. Same idempotency
// contract as TaskHandler.
type CommitmentHandler func(ctx context.Context, c *lobslawv1.AgentCommitment) error

// HandlerRegistry maps HandlerRef strings to concrete functions.
// Populated at boot by whoever wires the scheduler; mutable afterward
// so tests can swap handlers between iterations.
type HandlerRegistry struct {
	mu          sync.RWMutex
	tasks       map[string]TaskHandler
	commitments map[string]CommitmentHandler
	// idempotent holds the refs that opted into at-least-once. Absence
	// is the safe default: a handler nobody thought about keeps the
	// conservative at-most-once ordering.
	idempotent map[string]bool
}

func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		tasks:       make(map[string]TaskHandler),
		commitments: make(map[string]CommitmentHandler),
		idempotent:  make(map[string]bool),
	}
}

// RegisterTask installs a handler for ref. Overwrites any prior
// entry — the last-write-wins pattern matches the registry's use as
// a boot-time wiring site where the final write is authoritative.
func (r *HandlerRegistry) RegisterTask(ref string, h TaskHandler) error {
	if ref == "" {
		return fmt.Errorf("scheduler.HandlerRegistry: ref required")
	}
	if h == nil {
		return fmt.Errorf("scheduler.HandlerRegistry: handler required for ref %q", ref)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[ref] = h
	return nil
}

// RegisterCommitment installs a commitment handler. See RegisterTask.
//
// By default a commitment is completed BEFORE its handler runs, which
// is at-most-once delivery: a "remind me at 9am" that fires twice is
// worse than one that occasionally goes missing. Pass Idempotent() to
// invert that for handlers where the opposite is true.
func (r *HandlerRegistry) RegisterCommitment(ref string, h CommitmentHandler, opts ...HandlerOption) error {
	if ref == "" {
		return fmt.Errorf("scheduler.HandlerRegistry: ref required")
	}
	if h == nil {
		return fmt.Errorf("scheduler.HandlerRegistry: handler required for ref %q", ref)
	}
	var cfg handlerOptions
	for _, o := range opts {
		o(&cfg)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commitments[ref] = h
	if cfg.idempotent {
		r.idempotent[ref] = true
	} else {
		delete(r.idempotent, ref)
	}
	return nil
}

// HandlerOption tunes how the scheduler runs a handler.
type HandlerOption func(*handlerOptions)

type handlerOptions struct{ idempotent bool }

// Idempotent marks a handler as safe to run more than once, which
// flips its commitment to at-least-once: completion is applied AFTER
// the handler returns rather than before.
//
// The default exists to stop a reminder double-firing. That reasoning
// inverts for a handler that polls a provider for a job already
// submitted and already being billed: re-polling costs one cheap
// request, whereas losing the only poll orphans the job — it keeps
// running, the artifact is never collected, and the user never hears
// back. Between "might poll twice" and "might silently lose paid
// work", the extra poll is obviously preferable.
//
// Only mark a handler idempotent if running it twice is genuinely
// harmless. Anything that delivers a message to a human is not.
func Idempotent() HandlerOption {
	return func(o *handlerOptions) { o.idempotent = true }
}

// IsIdempotent reports whether ref opted into at-least-once.
func (r *HandlerRegistry) IsIdempotent(ref string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.idempotent[ref]
}

func (r *HandlerRegistry) GetTaskHandler(ref string) (TaskHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.tasks[ref]
	return h, ok
}

func (r *HandlerRegistry) GetCommitmentHandler(ref string) (CommitmentHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.commitments[ref]
	return h, ok
}
