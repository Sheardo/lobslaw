package node

import (
	"sync"
	"time"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/promptgen"
)

// Pinned memory is rendered into the system prompt on every turn, so
// it sits in the part of the request a provider caches. Reading it
// fresh each turn would mean every write invalidated the prefix for
// the turn after it — the block would be always-on and never cached,
// which is the worst of both.
//
// So the rendered form is frozen per session. Writes are durable
// immediately; the snapshot refreshes at the next session boundary.
// That is the whole trick, and it is hermes's, stated in their
// memory_tool.py:
//
//	Mid-session writes update files on disk immediately (durable) but
//	do NOT change the system prompt — this preserves the prefix cache
//	for the entire session.

// pinnedSnapshotTTL bounds how long a session holds a stale view.
//
// A session boundary is not observable from here — a Telegram
// conversation has no end — so "the session" is approximated by a
// window. Long enough that an ordinary exchange is one cached prefix;
// short enough that a fact recorded this morning is in play by
// lunchtime without anybody restarting anything.
const pinnedSnapshotTTL = 30 * time.Minute

type pinnedSnapshot struct {
	blocks  promptgen.PinnedBlocks
	takenAt time.Time
}

// pinnedProvider returns the per-session snapshot function the agent
// calls when assembling a prompt.
func (n *Node) pinnedProvider() func(sessionKey, userID string) promptgen.PinnedBlocks {
	if n.pinnedStore == nil {
		return nil
	}
	var (
		mu    sync.Mutex
		cache = map[string]pinnedSnapshot{}
	)

	return func(sessionKey, userID string) promptgen.PinnedBlocks {
		if userID == "" {
			// No principal, no profile. Rendering somebody else's
			// would be worse than rendering none.
			return promptgen.PinnedBlocks{}
		}
		// The blocks belong to a user; the freeze belongs to a
		// conversation. Keyed on both, so the same person on two
		// channels gets two snapshots — each has its own prompt prefix
		// being cached.
		//
		// In a group chat this means the prefix flips as speakers
		// alternate, and the cache hit rate goes with it. That is the
		// honest trade: a shared prefix would mean showing one
		// participant's profile to everybody.
		key := sessionKey + "|" + userID

		mu.Lock()
		snap, ok := cache[key]
		fresh := ok && time.Since(snap.takenAt) < pinnedSnapshotTTL
		mu.Unlock()
		if fresh {
			return snap.blocks
		}

		blocks := promptgen.PinnedBlocks{
			Profile: n.pinnedEntries(memory.PinnedProfile, userID),
			Notes:   n.pinnedEntries(memory.PinnedNotes, userID),
		}
		mu.Lock()
		cache[key] = pinnedSnapshot{blocks: blocks, takenAt: time.Now()}
		// Bounded so a busy deployment does not accumulate a snapshot
		// per conversation forever. Cheap because the blocks are
		// small, but unbounded is unbounded.
		if len(cache) > maxPinnedSnapshots {
			for k, v := range cache {
				if time.Since(v.takenAt) >= pinnedSnapshotTTL {
					delete(cache, k)
				}
			}
		}
		mu.Unlock()
		return blocks
	}
}

const maxPinnedSnapshots = 256

func (n *Node) pinnedEntries(kind memory.PinnedKind, userID string) []string {
	rec, err := n.pinnedStore.Get(kind, userID)
	if err != nil {
		n.log.Warn("pinned memory: read failed; rendering without it",
			"kind", kind, "user", userID, "err", err)
		return nil
	}
	return rec.Entries
}
