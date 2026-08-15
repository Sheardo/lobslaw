package config

import "sync/atomic"

// AtomicConfig is a typed wrapper around sync/atomic.Pointer for
// subsystems that want lock-free read access to a config value that
// the watcher can replace at runtime. Zero value is a valid empty
// holder; Load returns nil until something is Stored.
type AtomicConfig[T any] struct {
	ptr atomic.Pointer[T]
}

// Load returns the current value, or nil if nothing has been Stored.
// Callers must tolerate the nil — a subsystem can start reading
// before the first config has been published.
func (a *AtomicConfig[T]) Load() *T { return a.ptr.Load() }

// Store publishes v as the current value. Readers already inside
// Load keep the pointer they got; the swap only affects subsequent
// loads, so v must not be mutated after being stored.
func (a *AtomicConfig[T]) Store(v *T) { a.ptr.Store(v) }
