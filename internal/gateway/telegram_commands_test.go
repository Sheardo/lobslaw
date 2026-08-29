package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTelegramCommandParsing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text     string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{"/new", "new", "", true},
		{"/status extra args", "status", "extra args", true},
		// In a group, clients append @BotName so the message can be
		// aimed at one bot among several. Ignoring the suffix would
		// make every command unrecognised exactly where several bots
		// are likely to be.
		{"/new@LobSlawTestBot", "new", "", true},
		{"/status@LobSlawTestBot why", "status", "why", true},
		{"/NEW", "new", "", true},
		{"  /new  ", "new", "", true},

		// Not commands. A message that merely starts with a slash is
		// not an invocation, and answering it as one is worse than
		// ignoring it.
		{"/etc/hosts is the file", "", "", false},
		{"hello", "", "", false},
		{"", "", "", false},
		{"/", "", "", false},
		{"/ new", "", "", false},
		{"//", "", "", false},
		{"/what's up", "", "", false},
	}
	for _, tc := range cases {
		name, args, ok := telegramCommand(tc.text)
		if ok != tc.wantOK || name != tc.wantName || args != tc.wantArgs {
			t.Errorf("telegramCommand(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.text, name, args, ok, tc.wantName, tc.wantArgs, tc.wantOK)
		}
	}
}

// A command must not reach the model or take a turn lease. handleCommand
// reports whether it consumed the message, and the caller returns on
// true — /new in particular would otherwise queue behind the very
// conversation it exists to discard.
func TestTelegramHandleCommandConsumesOnlyCommands(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{}
	h := &TelegramHandler{
		cfg:      TelegramConfig{},
		log:      discardLogger(),
		commands: NewCommandSet(fakeAuthz{allow: true}, discardLogger()),
	}
	RegisterBuiltinCommands(h.commands, conv)

	// Ordinary text is not consumed, so the turn proceeds.
	if h.handleCommand(t.Context(), 1, "what is the weather", CommandRequest{}) {
		t.Error("plain text was consumed as a command")
	}
	if len(conv.forgotten) != 0 {
		t.Error("plain text reached a command handler")
	}

	// A handler with no command set never consumes, so a deployment
	// that did not wire one behaves exactly as before.
	bare := &TelegramHandler{cfg: TelegramConfig{}, log: discardLogger()}
	if bare.handleCommand(t.Context(), 1, "/new", CommandRequest{}) {
		t.Error("a handler with no command set consumed a command")
	}
}

// /start is sent automatically by every Telegram client on first
// contact. Intercepting it meant the first thing a new user heard from
// the bot was a complaint about a command they never typed.
func TestTelegramUnregisteredCommandReachesTheAgent(t *testing.T) {
	t.Parallel()

	cs := NewCommandSet(fakeAuthz{allow: true}, discardLogger())
	cs.Register(&Command{
		Name:       "new",
		SharedSafe: true,
		Handler:    func(context.Context, CommandRequest) (string, error) { return "ok", nil },
	})
	h := &TelegramHandler{commands: cs, log: discardLogger()}

	for _, text := range []string{"/start", "/deploy the thing", "/help me pick a model"} {
		if h.handleCommand(context.Background(), 1, text, CommandRequest{}) {
			t.Errorf("%q was swallowed as a command; it should reach the agent", text)
		}
	}
}

// A registered one is still intercepted, or the fall-through has simply
// disabled commands.
func TestTelegramRegisteredCommandIsStillHandled(t *testing.T) {
	t.Parallel()

	cs := NewCommandSet(fakeAuthz{allow: true}, discardLogger())
	ran := false
	cs.Register(&Command{
		Name:       "new",
		SharedSafe: true,
		Handler: func(context.Context, CommandRequest) (string, error) {
			ran = true
			return "ok", nil
		},
	})
	// An httptest Bot API, because handling a command ends in a reply
	// and the point of the assertion is that it got that far.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer srv.Close()
	h := &TelegramHandler{
		commands: cs,
		log:      discardLogger(),
		base:     srv.URL,
		client:   srv.Client(),
	}

	if !h.handleCommand(context.Background(), 1, "/new", CommandRequest{}) {
		t.Fatal("a registered command should be handled, not passed to the agent")
	}
	if !ran {
		t.Error("the command's handler never ran")
	}
}
