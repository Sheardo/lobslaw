package gateway

import (
	"context"
)

// telegramResponder is the Telegram side of the Responder contract.
// One per turn, because everything it does is scoped to a chat.
type telegramResponder struct {
	h      *TelegramHandler
	chatID int64
}

// Typing POSTs sendChatAction. Cheap — no message, just a presence
// signal — and errors are swallowed by the handler's own logging,
// because losing a typing indicator is a soft failure.
func (r *telegramResponder) Typing(context.Context) error {
	r.h.postJSON("sendChatAction", map[string]any{
		"chat_id": r.chatID,
		"action":  "typing",
	})
	return nil
}

func (r *telegramResponder) Interim(_ context.Context, text string) error {
	r.h.sendText(r.chatID, text)
	return nil
}

func (r *telegramResponder) Final(_ context.Context, text string) error {
	r.h.sendText(r.chatID, text)
	return nil
}

// startResponsivenessGuards adapts the handler's config onto the
// shared timers. Kept as a method so the call site reads the same as
// it did before the timers moved out of this package's Telegram half.
func (h *TelegramHandler) startResponsivenessGuards(ctx context.Context, chatID int64) (context.Context, func()) {
	return startResponsiveness(ctx, &telegramResponder{h: h, chatID: chatID}, ResponsivenessConfig{
		TypingInterval: h.cfg.TypingInterval,
		InterimTimeout: h.cfg.InterimTimeout,
		HardTimeout:    h.cfg.HardTimeout,
		Soul:           h.cfg.Soul,
	})
}
