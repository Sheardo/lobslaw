package memory

import "testing"

// Every bucket the code names must be one the store creates on open.
//
// A bucket declared as a constant and left out of allBuckets does not
// fail at startup — it fails on the first READ, with `bucket "groups"
// not found`, which reads like corruption rather than like a missing
// line in a list. Groups shipped that way: the registry was wired, the
// routes were mounted, and every request 500'd.
func TestEveryDeclaredBucketIsCreatedOnOpen(t *testing.T) {
	t.Parallel()

	declared := []string{
		BucketBots, BucketBotInbox, BucketGroups, BucketSessions,
		BucketScheduledTasks, BucketCommitments, BucketSoulTune,
		BucketUserPrefs, BucketPrompts, BucketPinned,
	}
	have := make(map[string]bool, len(allBuckets))
	for _, b := range allBuckets {
		have[b] = true
	}
	for _, b := range declared {
		if !have[b] {
			t.Errorf("bucket %q is declared but never created; every read of it will fail", b)
		}
	}
}
