package gateway

import (
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
