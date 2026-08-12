// Package ids generates the ULIDs used as record identifiers across
// lobslaw — episodic memories, commitments, scheduled tasks, audit
// entries, research runs.
//
// It exists because the obvious implementation is subtly wrong. IDs
// created in the same millisecond must sort monotonically, which
// requires them to SHARE one ulid.MonotonicReader — a fresh reader
// per call resets the counter and emits out-of-order IDs. But a
// shared MonotonicReader is explicitly not safe for concurrent use,
// and every ID here is minted from a request path that can run
// concurrently with itself: two channels, a scheduled task firing
// beside a chat turn, a research fan-out.
//
// Five call sites had independently copied the sharing half of that
// pattern and dropped the locking half. The failure mode is silent:
// racing readers can emit duplicate ULIDs, and records are keyed by
// ID in bbolt, so one memory or commitment quietly overwrites
// another. Centralising it means the next subsystem that needs an ID
// cannot reintroduce the bug.
package ids

import (
	cryptorand "crypto/rand"
	"sync"

	"github.com/oklog/ulid/v2"
)

// One process-wide source, guarded by our own mutex rather than
// ulid.LockedMonotonicReader.
//
// That wrapper locks only the entropy read, leaving the ulid.Now()
// clock read outside the critical section — so two goroutines can
// sample timestamps T and T+1 and then acquire in the opposite
// order. The T+1 caller reseeds the monotonic state; the T caller
// increments from that fresh seed but stamps its ID with T, and the
// two IDs land in the same millisecond with entropy running
// backwards. Holding the lock across both reads makes the sequence
// strictly increasing for every caller.
var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(cryptorand.Reader, 0)
)

// New returns a fresh ULID string: 26 characters of base-32, sorting
// lexicographically by creation time. Safe for concurrent use, and
// strictly increasing even when callers race.
func New() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	return ulid.MustNew(ulid.Now(), entropy).String()
}
