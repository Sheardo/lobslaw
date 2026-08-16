package node

import (
	"context"

	"github.com/jmylchreest/lobslaw/internal/memory"
)

// pinnedStoreAdapter satisfies compute.PinnedMemoryStore. The two
// packages cannot import each other, so the kind translation sits
// here — compute deals in strings because a typed enum would mean the
// dependency the adapter exists to avoid.
type pinnedStoreAdapter struct{ inner *memory.PinnedStore }

func (a pinnedStoreAdapter) Entries(kind, userID string) ([]string, error) {
	rec, err := a.inner.Get(memory.PinnedKind(kind), userID)
	if err != nil {
		return nil, err
	}
	return rec.Entries, nil
}

func (a pinnedStoreAdapter) Add(ctx context.Context, kind, userID, entry string) error {
	return a.inner.Add(ctx, memory.PinnedKind(kind), userID, entry)
}

func (a pinnedStoreAdapter) Replace(ctx context.Context, kind, userID, match, replacement string) error {
	return a.inner.Replace(ctx, memory.PinnedKind(kind), userID, match, replacement)
}

func (a pinnedStoreAdapter) Remove(ctx context.Context, kind, userID, match string) error {
	return a.inner.Remove(ctx, memory.PinnedKind(kind), userID, match)
}

func (a pinnedStoreAdapter) Usage(kind, userID string) (int, int, error) {
	return a.inner.Usage(memory.PinnedKind(kind), userID)
}
