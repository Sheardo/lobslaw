package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// R1 was marked done with none of its four acceptance boxes ticked.
// Two were already covered — a restart reloading history, and a REST
// client with and without a session_id. These are the two that were
// not.

// --- two nodes, one chat -----------------------------------------------

// Two gateway nodes serving the same chat must produce ONE coherent
// history, not two diverging ones.
//
// The risk is not persistence — that is the restart case, already
// tested. It is that each node keeps its OWN in-memory cache, so after
// B appends, A's Load could serve its stale cache and lose B's turn.
// Alternating is what exposes it; a single hand-off does not.
func TestTwoNodesAlternatingSeeOneHistory(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	ref := testRef()

	nodeA := newConversationLog(store, nil, ConversationConfig{}, nil)
	nodeB := newConversationLog(store, nil, ConversationConfig{}, nil)

	// A serves the first turn — and warms its own cache doing so.
	nodeA.Append(context.Background(), ref, "t1", []compute.Message{
		{Role: "user", Content: "my name is james"},
		{Role: "assistant", Content: "hello james"},
	})
	// B serves the second, having never seen the first.
	if got := nodeB.Load(context.Background(), ref).Messages; len(got) != 2 {
		t.Fatalf("node B saw %d messages from node A's turn; want 2", len(got))
	}
	nodeB.Append(context.Background(), ref, "t2", []compute.Message{
		{Role: "user", Content: "what is my name"},
		{Role: "assistant", Content: "james"},
	})

	// A serves the third. Its cache holds only its own turn; the
	// durable store holds both.
	got := nodeA.Load(context.Background(), ref).Messages
	if len(got) != 4 {
		t.Fatalf("node A saw %d messages after alternating; want 4:\n%+v", len(got), got)
	}
	var joined strings.Builder
	for _, m := range got {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "what is my name") {
		t.Errorf("node A's stale cache shadowed node B's turn:\n%s", joined.String())
	}
}

// And the same in the other direction, so the test is not passing
// because one particular node happened to be the writer.
func TestEitherNodeCanServeTheNextTurn(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	ref := testRef()

	nodeA := newConversationLog(store, nil, ConversationConfig{}, nil)
	nodeB := newConversationLog(store, nil, ConversationConfig{}, nil)

	nodeA.Append(context.Background(), ref, "t1",
		[]compute.Message{{Role: "user", Content: "first"}})
	nodeB.Append(context.Background(), ref, "t2",
		[]compute.Message{{Role: "user", Content: "second"}})
	nodeA.Append(context.Background(), ref, "t3",
		[]compute.Message{{Role: "user", Content: "third"}})

	for name, node := range map[string]*conversationLog{"A": nodeA, "B": nodeB} {
		got := node.Load(context.Background(), ref).Messages
		if len(got) != 3 {
			t.Errorf("node %s saw %d messages; want 3", name, len(got))
			continue
		}
		for i, want := range []string{"first", "second", "third"} {
			if got[i].Content != want {
				t.Errorf("node %s message %d = %q, want %q", name, i, got[i].Content, want)
			}
		}
	}
}

// A node whose durable write failed keeps its own turn in cache — and
// must NOT then serve that cache in place of the fuller durable
// history once the store is reachable again. Otherwise a brief
// follower period leaves one node permanently behind.
func TestACacheDoesNotShadowARicherDurableHistory(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	ref := testRef()

	node := newConversationLog(store, nil, ConversationConfig{}, nil)
	// Durable writes fail: the turn lands in cache only.
	store.appendErr = ErrSessionUnavailable
	node.Append(context.Background(), ref, "t1",
		[]compute.Message{{Role: "user", Content: "written while a follower"}})

	// Another node, with a working store, records two turns.
	store.appendErr = nil
	other := newConversationLog(store, nil, ConversationConfig{}, nil)
	other.Append(context.Background(), ref, "t2", []compute.Message{
		{Role: "user", Content: "recorded durably"},
		{Role: "assistant", Content: "and answered"},
	})

	got := node.Load(context.Background(), ref).Messages
	if len(got) != 2 {
		t.Fatalf("the follower's cache shadowed the durable history: %d messages\n%+v", len(got), got)
	}
	if got[0].Content != "recorded durably" {
		t.Errorf("got %+v; the durable history should win once it is available", got)
	}
}

// --- the compacted head survives ---------------------------------------

// R1: a conversation past max_messages retains a summary of the
// dropped prefix.
//
// The trim and the summary were each tested in isolation. What was
// not: that Load returns the summary ALONGSIDE the trimmed messages,
// which is the only way a fact stated in the dropped region reaches
// the next turn.
func TestASummarySurvivesTheTrimAndIsLoaded(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	ref := testRef()

	// The head is gone; only the summary remembers it.
	store.summaries[cacheKey(ref)] = "the user said their name is james"
	node := newConversationLog(store, nil, ConversationConfig{}, nil)
	node.Append(context.Background(), ref, "t9",
		[]compute.Message{{Role: "user", Content: "what did I say my name was?"}})

	got := node.Load(context.Background(), ref)
	if got.Summary == "" {
		t.Fatal("the summary was dropped; the compacted head is unrecoverable")
	}
	if !strings.Contains(got.Summary, "james") {
		t.Errorf("summary = %q; the fact from the dropped region is gone", got.Summary)
	}
	if len(got.Messages) == 0 {
		t.Error("the recent messages were lost alongside the summary")
	}
}

// A transcript that is ONLY a summary must still be served. After a
// long idle period the recent messages can age out entirely, and
// falling through to the cache there would discard the one thing that
// still remembers the conversation.
func TestASummaryAloneIsStillATranscript(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	ref := testRef()
	store.summaries[cacheKey(ref)] = "the user is called james"

	node := newConversationLog(store, nil, ConversationConfig{}, nil)
	got := node.Load(context.Background(), ref)
	if got.Summary == "" {
		t.Error("a summary-only transcript fell through to the empty cache")
	}
}
