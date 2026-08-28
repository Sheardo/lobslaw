package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func readerHandler(allowed ...string) *SlackHandler {
	return &SlackHandler{
		cfg: SlackConfig{AllowedChannels: allowed},
		log: discardLogger(),
		// Pointed at a closed port: every test below must refuse
		// BEFORE any network call, so reaching the API is itself a
		// failure.
		api: newSlackAPI("xoxb-test", "http://127.0.0.1:1", nil),
	}
}

// The allowlist has to bite at the tool boundary, not only on inbound
// events. Otherwise it governs what the agent HEARS while leaving what
// it can go and FETCH wide open.
func TestSlackReadRefusesChannelOutsideAllowlist(t *testing.T) {
	t.Parallel()

	h := readerHandler("C0ALLOWED")
	_, err := h.ReadConversation(context.Background(), "C0SECRET", 10)
	if !errors.Is(err, ErrSlackChannelNotAllowed) {
		t.Fatalf("err = %v, want ErrSlackChannelNotAllowed", err)
	}
	if _, err := h.ReadThread(context.Background(), "C0SECRET", "1.1", 10); !errors.Is(err, ErrSlackChannelNotAllowed) {
		t.Fatalf("ReadThread err = %v, want ErrSlackChannelNotAllowed", err)
	}
	if _, err := h.SearchConversations(context.Background(), "q", []string{"C0SECRET"}, 5); !errors.Is(err, ErrSlackChannelNotAllowed) {
		t.Fatalf("Search err = %v, want ErrSlackChannelNotAllowed", err)
	}
}

// An empty allowlist is closed here too, matching the event path.
func TestSlackReadEmptyAllowlistRefusesEverything(t *testing.T) {
	t.Parallel()

	h := readerHandler()
	if _, err := h.ReadConversation(context.Background(), "C0ANY", 10); !errors.Is(err, ErrSlackChannelNotAllowed) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// A wildcard allowlist says the bot may act wherever it is invited. It
// does not say one tool call may walk the whole workspace, and an
// unbounded fan-out is exactly what a confused model would ask for.
func TestSlackSearchRefusesUnboundedWildcardFanout(t *testing.T) {
	t.Parallel()

	h := readerHandler("*")
	_, err := h.SearchConversations(context.Background(), "anything", nil, 5)
	if err == nil {
		t.Fatal("a wildcard allowlist allowed a search with no named channels")
	}
	if !strings.Contains(err.Error(), "must name the conversations") {
		t.Errorf("err = %v, want an explanation of what to do instead", err)
	}
}

// With explicit channels a nil refs list is well defined: search them.
func TestSlackSearchTargetsFallBackToAllowlist(t *testing.T) {
	t.Parallel()

	h := readerHandler("C0ONE", "C0TWO")
	got, err := h.searchTargets(context.Background(), nil)
	if err != nil {
		t.Fatalf("searchTargets: %v", err)
	}
	if len(got) != 2 || got[0] != "C0ONE" || got[1] != "C0TWO" {
		t.Fatalf("targets = %v, want the allowlist", got)
	}
}

func TestSlackSearchRequiresAQuery(t *testing.T) {
	t.Parallel()

	h := readerHandler("C0ONE")
	if _, err := h.SearchConversations(context.Background(), "   ", nil, 5); err == nil {
		t.Fatal("a blank query was accepted")
	}
}

// Ids are used as-is; anything else is a name needing resolution. The
// distinction decides whether a lookup happens at all.
func TestLooksLikeChannelID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"C0ALLOWED", "D0BPM5D6QA3", "G0PRIVATE"} {
		if !looksLikeChannelID(id) {
			t.Errorf("%q not recognised as an id", id)
		}
	}
	for _, name := range []string{"general", "#general", "General", "c", ""} {
		if looksLikeChannelID(name) {
			t.Errorf("%q wrongly treated as an id", name)
		}
	}
}

// History carries joins, edits and the bot's own posts. Feeding those
// back would have the agent reading itself.
func TestIsReadableMessage(t *testing.T) {
	t.Parallel()

	if !isReadableMessage(slackMessage{Text: "hello"}) {
		t.Error("a plain human message was filtered out")
	}
	for _, m := range []slackMessage{
		{Text: "joined", Subtype: "channel_join"},
		{Text: "hi", BotID: "B1"},
		{Text: "   "},
		{},
	} {
		if isReadableMessage(m) {
			t.Errorf("%+v was treated as readable", m)
		}
	}
}

func TestReverseOrdersOldestFirst(t *testing.T) {
	t.Parallel()

	// conversations.history returns newest-first; the agent reads a
	// transcript, which only makes sense oldest-first.
	msgs := []SlackTranscriptMessage{{TS: "3"}, {TS: "2"}, {TS: "1"}}
	reverse(msgs)
	if msgs[0].TS != "1" || msgs[2].TS != "3" {
		t.Fatalf("order = %v", []string{msgs[0].TS, msgs[1].TS, msgs[2].TS})
	}
}
