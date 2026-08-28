package gateway

import (
	"strings"
	"testing"
	"time"
)

// The allowlist is the coarse gate, and its empty case is the one that
// matters: a zero value that means "open" would hand a bot every
// conversation it was ever invited to.
func TestSlackAllowedChannels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		allowed []string
		channel string
		want    bool
	}{
		{"empty is closed", nil, "C123", false},
		{"empty slice is closed", []string{}, "C123", false},
		{"wildcard opens everything", []string{"*"}, "C123", true},
		{"exact match", []string{"C123", "C456"}, "C456", true},
		{"non-match refused", []string{"C123"}, "C999", false},
		{"whitespace tolerated", []string{" C123 "}, "C123", true},
		{"dm id like any other", []string{"D0ALICE"}, "D0ALICE", true},
		{"dm not implicitly allowed", []string{"C123"}, "D0ALICE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &SlackHandler{cfg: SlackConfig{AllowedChannels: tc.allowed}}
			if got := h.isAllowedChannel(tc.channel); got != tc.want {
				t.Errorf("isAllowedChannel(%q) = %v, want %v", tc.channel, got, tc.want)
			}
		})
	}
}

func TestSlackResolveScope(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{cfg: SlackConfig{
		UserScopes: map[string]string{"U0ALICE": "owner"},
	}}
	if scope, ok := h.resolveScope("U0ALICE"); !ok || scope != "owner" {
		t.Errorf("mapped user = (%q,%v), want (owner,true)", scope, ok)
	}
	// Unmapped with no fallback is a drop, matching Telegram.
	if _, ok := h.resolveScope("U0BOB"); ok {
		t.Error("unmapped user was admitted with no unknown_user_scope")
	}

	open := &SlackHandler{cfg: SlackConfig{UnknownUserScope: "public"}}
	if scope, ok := open.resolveScope("U0BOB"); !ok || scope != "public" {
		t.Errorf("fallthrough = (%q,%v), want (public,true)", scope, ok)
	}
}

// The loop-prevention filter. Getting this wrong in a channel is not a
// cosmetic bug — the bot answers itself forever.
func TestSlackWantsEvent(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{botUserID: "U0BOT"}
	cases := []struct {
		name string
		ev   slackEvent
		want bool
	}{
		{"plain message", slackEvent{Type: "message", User: "U0ALICE", Text: "hi"}, true},
		{"app mention", slackEvent{Type: "app_mention", User: "U0ALICE", Text: "<@U0BOT> hi"}, true},
		{"our own message", slackEvent{Type: "message", User: "U0BOT", Text: "hi"}, false},
		{"another bot", slackEvent{Type: "message", User: "U0X", BotID: "B1", Text: "hi"}, false},
		{"edit", slackEvent{Type: "message", Subtype: "message_changed", User: "U0ALICE", Text: "hi"}, false},
		{"join", slackEvent{Type: "message", Subtype: "channel_join", User: "U0ALICE", Text: "joined"}, false},
		{"no user", slackEvent{Type: "message", Text: "hi"}, false},
		{"blank text", slackEvent{Type: "message", User: "U0ALICE", Text: "   "}, false},
		{"unrelated type", slackEvent{Type: "reaction_added", User: "U0ALICE", Text: "hi"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.wantsEvent(tc.ev); got != tc.want {
				t.Errorf("wantsEvent = %v, want %v", got, tc.want)
			}
		})
	}
}

// A thread is its own conversation. Without this every thread in a
// busy channel interleaves into one transcript.
func TestSlackConversationID(t *testing.T) {
	t.Parallel()

	// A top-level CHANNEL message is answered in a thread rooted at
	// itself, so that thread is the conversation from the first turn.
	if got := conversationID(slackEvent{ChannelType: "channel", Channel: "C123", TS: "1.1"}); got != "C123/1.1" {
		t.Errorf("channel message = %q, want C123/1.1", got)
	}
	if got := conversationID(slackEvent{ChannelType: "channel", Channel: "C123", TS: "2.2", ThreadTS: "1.1"}); got != "C123/1.1" {
		t.Errorf("threaded message = %q, want C123/1.1", got)
	}
	// A DM is answered inline, so the conversation is the channel.
	if got := conversationID(slackEvent{ChannelType: "im", Channel: "D123", TS: "1.1"}); got != "D123" {
		t.Errorf("dm = %q, want D123", got)
	}
	// Except when the user explicitly threaded it, which is genuinely a
	// separate conversation.
	if got := conversationID(slackEvent{ChannelType: "im", Channel: "D123", TS: "2.2", ThreadTS: "1.1"}); got != "D123/1.1" {
		t.Errorf("threaded dm = %q, want D123/1.1", got)
	}
}

// The invariant behind the split-thread bug: the conversation a turn is
// stored under has to be the conversation the answer lands in. Derived
// separately they disagreed exactly once — on the first message of a
// channel thread — and the bot forgot the message that started the
// thread it was standing in.
func TestSlackConversationFollowsTheReply(t *testing.T) {
	t.Parallel()

	// Walk a channel thread the way Slack delivers it: a top-level
	// mention, then a reply inside the thread the bot created.
	opening := slackEvent{ChannelType: "channel", Channel: "C1", TS: "100"}
	followUp := slackEvent{ChannelType: "channel", Channel: "C1", TS: "200", ThreadTS: replyThread(opening)}

	if conversationID(opening) != conversationID(followUp) {
		t.Fatalf("thread split: opening %q, follow-up %q",
			conversationID(opening), conversationID(followUp))
	}
	// And the conversation names the thread the reply went to.
	if want := slackConversationID("C1", replyThread(opening)); conversationID(opening) != want {
		t.Errorf("conversation %q does not match the reply thread %q",
			conversationID(opening), want)
	}

	// A DM stays one conversation across turns, because replies stay
	// inline and nothing acquires a thread_ts.
	dm1 := slackEvent{ChannelType: "im", Channel: "D1", TS: "100"}
	dm2 := slackEvent{ChannelType: "im", Channel: "D1", TS: "200"}
	if conversationID(dm1) != conversationID(dm2) {
		t.Fatalf("dm split across turns: %q vs %q", conversationID(dm1), conversationID(dm2))
	}

	// Two separate channel threads remain separate.
	other := slackEvent{ChannelType: "channel", Channel: "C1", TS: "300"}
	if conversationID(opening) == conversationID(other) {
		t.Error("two distinct threads collapsed into one conversation")
	}
}

// A colon here is refused by memory.sessionID, whose bolt key range is
// "<channel>:<channel_id>:<seq>". The failure is not loud: the durable
// write is rejected, the handler falls back to in-memory history, and
// every Slack thread quietly loses its transcript on restart. Found
// exactly that way against a live workspace.
func TestSlackConversationIDHasNoColon(t *testing.T) {
	t.Parallel()

	// A real Slack thread ts, which contains a dot but must not
	// introduce a colon once joined to the channel.
	got := slackConversationID("D0BPM5D6QA3", "1786640128.065169")
	if strings.Contains(got, ":") {
		t.Fatalf("conversation id %q contains ':' and will be refused by the session store", got)
	}
	if got != "D0BPM5D6QA3/1786640128.065169" {
		t.Errorf("got %q", got)
	}
	// The unthreaded form is the bare channel, colon-free by nature.
	if strings.Contains(slackConversationID("C123", ""), ":") {
		t.Error("the unthreaded conversation id contains ':'")
	}
}

// Drives the recall rule from Phase 0, so the asymmetry matters: an
// unknown channel_type must count as shared.
func TestSlackSharedConversation(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"im":      false,
		"channel": true,
		"group":   true,
		"mpim":    true,
		"":        true, // unknown → shared, the cheap way to be wrong
	}
	for kind, want := range cases {
		if got := isSharedSlackConversation(slackEvent{ChannelType: kind}); got != want {
			t.Errorf("channel_type %q: shared = %v, want %v", kind, got, want)
		}
	}
}

func TestSlackReplyThread(t *testing.T) {
	t.Parallel()

	// A DM answers inline — there is no room to keep tidy.
	if got := replyThread(slackEvent{ChannelType: "im", TS: "1.1"}); got != "" {
		t.Errorf("dm reply thread = %q, want empty", got)
	}
	// A channel message starts a thread off itself.
	if got := replyThread(slackEvent{ChannelType: "channel", TS: "1.1"}); got != "1.1" {
		t.Errorf("channel reply thread = %q, want 1.1", got)
	}
	// A threaded message stays in its thread rather than starting one.
	if got := replyThread(slackEvent{ChannelType: "channel", TS: "2.2", ThreadTS: "1.1"}); got != "1.1" {
		t.Errorf("in-thread reply = %q, want 1.1", got)
	}
}

// The same handle in two workspaces is two accounts. Merging them
// would join two sets of memories that were never meant to meet.
func TestSlackUserIdentityIsTeamScoped(t *testing.T) {
	t.Parallel()

	a := slackUserIdentity("T0ONE", "U0ALICE")
	b := slackUserIdentity("T0TWO", "U0ALICE")
	if a == b {
		t.Fatalf("same user id in two teams collapsed to one principal: %q", a)
	}
	if got := slackUserIdentity("", ""); got != "slack-unknown" {
		t.Errorf("empty identity = %q, want slack-unknown", got)
	}
}

func TestStripBotMention(t *testing.T) {
	t.Parallel()

	if got := stripBotMention("<@U0BOT> what is the status?", "U0BOT"); got != "what is the status?" {
		t.Errorf("got %q", got)
	}
	// A bare mention leaves nothing, which the caller must cope with.
	if got := stripBotMention("<@U0BOT>", "U0BOT"); got != "" {
		t.Errorf("bare mention = %q, want empty", got)
	}
	// Unknown bot id leaves the text alone rather than mangling it.
	if got := stripBotMention("<@U0BOT> hi", ""); got != "<@U0BOT> hi" {
		t.Errorf("got %q", got)
	}
}

// Slack redelivers anything it did not see acked, and a dropped ack is
// normal during a reconnect. Without dedup that re-runs a turn's tool
// calls.
func TestSlackFirstSeenDeduplicates(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{seen: make(map[string]time.Time)}
	key := eventKey("T0", slackEvent{Channel: "C1", TS: "1.1"})

	if !h.firstSeen(key) {
		t.Fatal("first delivery was treated as a duplicate")
	}
	if h.firstSeen(key) {
		t.Fatal("redelivery was not deduplicated")
	}
	// A different message in the same channel is not a duplicate.
	if !h.firstSeen(eventKey("T0", slackEvent{Channel: "C1", TS: "2.2"})) {
		t.Fatal("a distinct event was treated as a duplicate")
	}
}

func TestSlackEventKeyIncludesTeam(t *testing.T) {
	t.Parallel()

	a := eventKey("T0ONE", slackEvent{Channel: "C1", TS: "1.1"})
	b := eventKey("T0TWO", slackEvent{Channel: "C1", TS: "1.1"})
	if a == b {
		t.Fatal("two workspaces collided on one event key")
	}
}

// Both tokens are load-bearing and neither substitutes for the other,
// so construction refuses rather than producing a channel that cannot
// connect or cannot reply.
func TestNewSlackHandlerRequiresBothTokens(t *testing.T) {
	t.Parallel()

	// Both token checks run before the agent check, so a nil agent
	// still exercises the branch under test.
	if _, err := NewSlackHandler(SlackConfig{AppToken: "xapp-1"}, nil); err == nil {
		t.Error("missing bot token was accepted")
	}
	if _, err := NewSlackHandler(SlackConfig{BotToken: "xoxb-1"}, nil); err == nil {
		t.Error("missing app token was accepted")
	}
}
