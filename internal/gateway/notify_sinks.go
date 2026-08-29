package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// TelegramSink adapts a TelegramHandler to the notify.Sink
// interface. Wraps Send so address-as-string (the channel-agnostic
// abstraction) becomes chat_id-as-int64 (Telegram's wire shape).
type TelegramSink struct {
	Handler *TelegramHandler
}

func (s *TelegramSink) ChannelType() string { return "telegram" }

func (s *TelegramSink) Deliver(_ context.Context, address, body string) error {
	if s.Handler == nil {
		return errors.New("telegram sink: handler not wired")
	}
	chatID, err := strconv.ParseInt(address, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram sink: address %q must parse as int64 chat_id: %w", address, err)
	}
	return s.Handler.Send(chatID, body)
}

// SlackSink adapts a SlackHandler to the notify.Sink interface.
//
// The address is a Slack conversation id, and chat.postMessage accepts
// a user id there directly — it opens the DM itself — so a proactive
// notification reaches a person without the sink needing to know
// whether it is addressing a channel or a human.
type SlackSink struct {
	Handler *SlackHandler
}

func (s *SlackSink) ChannelType() string { return ChannelSlack }

func (s *SlackSink) Deliver(ctx context.Context, address, body string) error {
	if s.Handler == nil {
		return errors.New("slack sink: handler not wired")
	}
	if address == "" {
		return errors.New("slack sink: empty address")
	}
	// No thread: a notification is a new topic, not a reply to
	// whatever the last conversation happened to be about.
	return s.Handler.api.postMessage(ctx, address, "", body)
}

// RESTSink is a placeholder — REST is request/response and can't
// push asynchronously to an already-disconnected client. The
// originator-channel reply path doesn't go through here; the
// gateway's existing reply mechanism handles that. Broadcasts to
// REST users are dropped with a warning until we add a webhook
// callback (operator-supplied URL we POST to).
type RESTSink struct{}

func (s *RESTSink) ChannelType() string { return "rest" }

func (s *RESTSink) Deliver(_ context.Context, address, body string) error {
	return fmt.Errorf("rest sink: REST broadcast not supported (address=%q); add a webhook callback URL to deliver async REST messages", address)
}
