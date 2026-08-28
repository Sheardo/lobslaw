package gateway

import (
	"context"
)

// slackResponder is the Slack side of the Responder contract. One per
// turn, because everything it does is scoped to a conversation — and,
// in a channel, to a thread.
type slackResponder struct {
	h       *SlackHandler
	channel string
	thread  string
}

// Typing is a no-op.
//
// Slack has no equivalent of Telegram's sendChatAction for a bot
// posting into a channel: the typing indicator is a real-time-messaging
// affordance for human users, and the app has no reactions:write to
// stand one in for it. Returning nil rather than faking a message is
// the honest answer — the interim message below is what actually tells
// the user something is happening.
func (r *slackResponder) Typing(context.Context) error { return nil }

func (r *slackResponder) Interim(ctx context.Context, text string) error {
	r.h.sendText(ctx, r.channel, r.thread, text)
	return nil
}

func (r *slackResponder) Final(ctx context.Context, text string) error {
	r.h.sendText(ctx, r.channel, r.thread, text)
	return nil
}

// startResponsivenessGuards adapts the handler's config onto the
// shared timers, exactly as the Telegram handler does. The hard
// timeout matters more here than there: Slack has already been acked,
// so a turn that never finishes leaves the user with silence and no
// error, and nothing upstream will time it out for us.
func (h *SlackHandler) startResponsivenessGuards(ctx context.Context, channel, thread string) (context.Context, func()) {
	return startResponsiveness(ctx, &slackResponder{h: h, channel: channel, thread: thread}, ResponsivenessConfig{
		TypingInterval: -1, // no typing concept; disable rather than spin a timer
		InterimTimeout: h.cfg.InterimTimeout,
		HardTimeout:    h.cfg.HardTimeout,
		Soul:           h.cfg.Soul,
	})
}
