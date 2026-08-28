package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

type fakeAuthz struct{ allow bool }

func (f fakeAuthz) AllowsCommand(context.Context, *types.Claims, string) bool { return f.allow }

type fakeConv struct {
	forgotten  []SessionRef
	forgetErr  error
	transcript Transcript
}

func (f *fakeConv) Forget(_ context.Context, ref SessionRef) error {
	f.forgotten = append(f.forgotten, ref)
	return f.forgetErr
}
func (f *fakeConv) Load(context.Context, SessionRef) Transcript { return f.transcript }

func newTestSet(allow bool, conv ConversationResetter) *CommandSet {
	cs := NewCommandSet(fakeAuthz{allow: allow}, discardLogger())
	RegisterBuiltinCommands(cs, conv)
	return cs
}

// A command is a privileged operation — /new destroys a transcript —
// so an unwired authorizer must refuse rather than wave it through.
func TestCommandSetWithoutAuthorizerRefuses(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{}
	cs := NewCommandSet(nil, discardLogger())
	RegisterBuiltinCommands(cs, conv)

	out := cs.Dispatch(context.Background(), CommandRequest{Name: "new"})
	if !strings.Contains(out, "not authorised") {
		t.Fatalf("got %q, want a refusal", out)
	}
	if len(conv.forgotten) != 0 {
		t.Fatal("the handler ran despite there being no authorizer")
	}
}

func TestCommandSetPolicyDenyStopsTheHandler(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{}
	cs := newTestSet(false, conv)

	out := cs.Dispatch(context.Background(), CommandRequest{Name: "new"})
	if !strings.Contains(out, "not allowed") {
		t.Fatalf("got %q, want a denial", out)
	}
	if len(conv.forgotten) != 0 {
		t.Fatal("a denied command still reset the conversation")
	}
}

func TestCommandNewForgetsTheConversation(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{}
	cs := newTestSet(true, conv)
	ref := SessionRef{Channel: "slack", ChannelID: "C1/1.1", UserID: "james"}

	out := cs.Dispatch(context.Background(), CommandRequest{Name: "new", Session: ref})
	if len(conv.forgotten) != 1 || conv.forgotten[0] != ref {
		t.Fatalf("forgot %v, want exactly %v", conv.forgotten, ref)
	}
	if !strings.Contains(out, "fresh") {
		t.Errorf("reply = %q", out)
	}
}

// A failing handler has to say so. Silence is indistinguishable from
// the bot ignoring the user, who then retries a destructive command.
func TestCommandFailureIsReported(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{forgetErr: errors.New("raft unavailable")}
	cs := newTestSet(true, conv)

	out := cs.Dispatch(context.Background(), CommandRequest{Name: "new"})
	if !strings.Contains(out, "raft unavailable") {
		t.Fatalf("got %q, want the underlying error", out)
	}
}

// whoami prints identity, which is nobody else's business.
func TestSharedOnlyCommandsRefusedInARoom(t *testing.T) {
	t.Parallel()

	cs := newTestSet(true, &fakeConv{})

	out := cs.Dispatch(context.Background(), CommandRequest{Name: "whoami", Shared: true})
	if !strings.Contains(out, "direct message") {
		t.Fatalf("got %q, want a refusal in a shared conversation", out)
	}
	// The same command in a DM answers.
	out = cs.Dispatch(context.Background(), CommandRequest{
		Name:    "whoami",
		Claims:  &types.Claims{Scope: "owner", Roles: []string{"operator"}},
		Session: SessionRef{Channel: "slack", ChannelID: "D1", UserID: "james"},
	})
	if !strings.Contains(out, "james") || !strings.Contains(out, "operator") {
		t.Errorf("whoami in a DM = %q", out)
	}
}

func TestUnknownCommandPointsAtHelp(t *testing.T) {
	t.Parallel()

	cs := newTestSet(true, &fakeConv{})
	if out := cs.Dispatch(context.Background(), CommandRequest{Name: "nope"}); !strings.Contains(out, "help") {
		t.Errorf("got %q", out)
	}
	// A bare prefix resolves to an empty name, which is somebody who
	// typed "/lobslaw" and stopped.
	if out := cs.Dispatch(context.Background(), CommandRequest{Name: ""}); !strings.Contains(out, "help") {
		t.Errorf("empty command = %q", out)
	}
}

// The leading slash is the channel's syntax, not the command's name.
func TestDispatchTolueratesLeadingSlash(t *testing.T) {
	t.Parallel()

	conv := &fakeConv{}
	cs := newTestSet(true, conv)
	cs.Dispatch(context.Background(), CommandRequest{Name: "/new"})
	if len(conv.forgotten) != 1 {
		t.Fatal("a slash-prefixed command did not dispatch")
	}
}

// Both invocation shapes have to work: the umbrella form, so adding a
// command costs nothing in Slack's UI, and the flat form for an
// operator who registered one directly.
func TestSplitSlashCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		command, text, wantName, wantArgs string
	}{
		{"/lobslaw", "new", "new", ""},
		{"/lobslaw", "status extra args", "status", "extra args"},
		{"/lobslaw", "", "", ""},
		{"/status", "", "status", ""},
		{"/status", "some args", "status", "some args"},
		{"/LobSlaw", "NEW", "new", ""},
		{"/lobslaw", "  new  ", "new", ""},
	}
	for _, tc := range cases {
		name, args := splitSlashCommand(tc.command, tc.text, "lobslaw")
		if name != tc.wantName || args != tc.wantArgs {
			t.Errorf("splitSlashCommand(%q,%q) = (%q,%q), want (%q,%q)",
				tc.command, tc.text, name, args, tc.wantName, tc.wantArgs)
		}
	}
}

// No channel_type on a slash command payload, so the DM test is by id
// prefix and anything unrecognised must count as having an audience.
func TestSlackChannelIsDM(t *testing.T) {
	t.Parallel()

	if !slackChannelIsDM("D0BPM5D6QA3") {
		t.Error("a D-prefixed id was not recognised as a DM")
	}
	for _, id := range []string{"C123", "G123", "", "X999"} {
		if slackChannelIsDM(id) {
			t.Errorf("%q was treated as a DM", id)
		}
	}
}
