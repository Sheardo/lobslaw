package notify

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type plainSink struct{ got string }

func (p *plainSink) ChannelType() string { return "plain" }
func (p *plainSink) Deliver(_ context.Context, _, body string) error {
	p.got = body
	return nil
}

type identitySink struct{ body, sender string }

func (i *identitySink) ChannelType() string { return "identity" }
func (i *identitySink) Deliver(_ context.Context, _, body string) error {
	i.body = body
	return nil
}
func (i *identitySink) DeliverFrom(_ context.Context, _, body, sender string) error {
	i.body, i.sender = body, sender
	return nil
}

// Attribution reaches a channel in the form that channel can render.
//
// A sink that cannot show WHO sent something needs the name prefixed
// into the text. A sink that can — Slack, posting under a per-message
// display name — must NOT get the prefix as well, or the name appears
// twice: once in the message header and once at the start of the
// sentence.
func TestAttributionMatchesWhatTheChannelCanRender(t *testing.T) {
	t.Parallel()

	t.Run("a plain sink gets the name in the text", func(t *testing.T) {
		sink := &plainSink{}
		if err := deliverTo(context.Background(), sink, "addr", "deploy finished", "devops", ""); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		if sink.got != "devops: deploy finished" {
			t.Errorf("body = %q, want the sender prefixed", sink.got)
		}
	})

	t.Run("an identity-aware sink gets the name separately", func(t *testing.T) {
		sink := &identitySink{}
		if err := deliverTo(context.Background(), sink, "addr", "deploy finished", "devops", ""); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		if sink.sender != "devops" {
			t.Errorf("sender = %q, want it passed through", sink.sender)
		}
		if strings.Contains(sink.body, "devops:") {
			t.Errorf("body = %q — the name is in the text AND the header", sink.body)
		}
	})

	t.Run("no sender leaves the body alone", func(t *testing.T) {
		sink := &plainSink{}
		if err := deliverTo(context.Background(), sink, "addr", "just a message", "", ""); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		if sink.got != "just a message" {
			t.Errorf("body = %q, want it untouched", sink.got)
		}
	})
}

// A message somebody else asked for has to say so.
//
// The moment more than one person can talk to the same team, "prepare
// a report and send it to James" is an ordinary request — and without
// provenance James receives a message from a bot with no way to tell
// whether he asked for it, a colleague did, or it arrived unprompted.
// The last reading is the dangerous one: anyone who can talk to a bot
// could otherwise make it send messages that read as though the system
// originated them.
func TestAMessageSaysWhoAskedForIt(t *testing.T) {
	t.Parallel()

	t.Run("a third party is named", func(t *testing.T) {
		sink := &plainSink{}
		if err := deliverTo(context.Background(), sink, "addr",
			"Here is the Q3 report.", "coordinator", "Sam"); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		if !strings.Contains(sink.got, "Sam asked me to send you this") {
			t.Errorf("body = %q, want it to name who asked", sink.got)
		}
		// The message is the point; the provenance is the footnote.
		if !strings.HasPrefix(sink.got, "coordinator: Here is the Q3 report.") {
			t.Errorf("body = %q, want the report first", sink.got)
		}
	})

	t.Run("an identity-aware channel still gets it in the body", func(t *testing.T) {
		sink := &identitySink{}
		if err := deliverTo(context.Background(), sink, "addr",
			"Here is the report.", "coordinator", "Sam"); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		// Slack renders the BOT in the header; who asked is a
		// different fact with no slot of its own.
		if !strings.Contains(sink.body, "Sam asked me") {
			t.Errorf("body = %q — provenance lost on a channel that shows a sender", sink.body)
		}
		if sink.sender != "coordinator" {
			t.Errorf("sender = %q", sink.sender)
		}
	})

	t.Run("nothing is added when nobody asked", func(t *testing.T) {
		sink := &plainSink{}
		if err := deliverTo(context.Background(), sink, "addr",
			"Nightly check passed.", "devops", ""); err != nil {
			t.Fatalf("deliverTo: %v", err)
		}
		if strings.Contains(sink.got, "asked me") {
			t.Errorf("body = %q — a routine was attributed to somebody", sink.got)
		}
	})
}

// "You asked for this" is noise on something you asked for, and noise
// in that position trains people to stop reading the line that matters.
func TestYouAreNotToldYouAskedForYourOwnMessage(t *testing.T) {
	t.Parallel()

	s := &Service{logger: discardLog()}
	if got := s.describeRequester(context.Background(), "user:james", "james"); got != "" {
		t.Errorf("describeRequester = %q, want empty for the recipient themselves", got)
	}
	if got := s.describeRequester(context.Background(), "user:sam", "james"); got != "sam" {
		t.Errorf("describeRequester = %q, want the requester named", got)
	}
	if got := s.describeRequester(context.Background(), "", "james"); got != "" {
		t.Errorf("describeRequester = %q, want empty when nobody asked", got)
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
