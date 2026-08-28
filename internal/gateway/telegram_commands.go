package gateway

import (
	"context"
	"strings"
)

// Telegram's half of the shared command surface.
//
// The dispatcher was built channel-agnostic so "/new" means one thing
// everywhere rather than one thing per channel. Until this, only Slack
// used it — which made the claim true and the benefit theoretical.

// telegramCommand parses a leading bot command out of a message.
//
// Telegram's own syntax, which is not quite anyone else's: a command is
// a leading "/name", and in a GROUP clients append "@BotName" so the
// message can be aimed at one of several bots in the room. Ignoring the
// suffix would make every command silently unrecognised in exactly the
// place several bots are likely to be.
//
// ok is false for anything that is not a command, including a message
// that merely starts with a slash — "/etc/hosts is the file" is not an
// invocation, and answering it as one would be worse than ignoring it.
func telegramCommand(text string) (name, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || len(text) < 2 {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text[1:], " ")
	// Strip the @BotName a group client appends.
	if at := strings.IndexByte(head, '@'); at >= 0 {
		head = head[:at]
	}
	head = strings.ToLower(strings.TrimSpace(head))
	if head == "" || !isCommandName(head) {
		return "", "", false
	}
	return head, strings.TrimSpace(rest), true
}

// isCommandName keeps a path like "/etc/hosts" or a date like "/2026"
// from being read as an invocation. Telegram's own rule is letters,
// digits and underscores, which is narrow enough to be worth enforcing
// rather than handing anything to the dispatcher and letting it say
// "unknown command" to somebody who never typed one.
func isCommandName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_':
		default:
			return false
		}
	}
	return true
}

// handleCommand runs a slash command and replies. Reports whether the
// message WAS a command, so the caller can skip the turn entirely: a
// command must not take a turn lease, load a transcript, or reach the
// model.
func (h *TelegramHandler) handleCommand(ctx context.Context, chatID int64, text string, req CommandRequest) bool {
	if h.commands == nil {
		return false
	}
	name, args, ok := telegramCommand(text)
	if !ok {
		return false
	}
	req.Name, req.Args = name, args
	h.sendText(chatID, h.commands.Dispatch(ctx, req))
	return true
}
