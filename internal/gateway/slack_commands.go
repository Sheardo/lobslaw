package gateway

import (
	"context"
	"strings"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// defaultSlashPrefix is the umbrella command a deployment registers in
// Slack's app UI.
//
// One registration rather than one per command, because every slash
// command has to be typed into that UI by hand: with an umbrella,
// adding a command to the CommandSet costs nothing on the Slack side,
// and without one every new command needs an operator to go and
// register it before anybody can run it.
//
// Directly-registered commands still work — "/status" dispatches as
// "status" — so an operator who prefers the flat form can have it.
const defaultSlashPrefix = "lobslaw"

// handleSlashCommand runs one slash command and replies to the caller.
//
// Already acked by the read loop, so nothing here races Slack's three
// second deadline. That is the whole reason the ack is unconditional
// and early: a command that has to load a transcript would otherwise
// be a coin flip against the timeout.
func (h *SlackHandler) handleSlashCommand(ctx context.Context, sc slackSlashCommand) {
	if h.commands == nil {
		h.log.Warn("slack: slash command arrived but no command set is wired",
			"command", sc.Command)
		return
	}
	if !h.isAllowedChannel(sc.ChannelID) {
		// A slash command reaches the app even from a conversation the
		// bot is not a member of, so this is an authorisation decision
		// rather than an impossible case. Answered ephemerally: the
		// person typed something and deserves to know it went nowhere,
		// but the room did not ask.
		h.log.Info("slack: slash command from a channel not in allowed_channels",
			"channel", sc.ChannelID, "user", sc.UserID, "command", sc.Command)
		h.replyToCommand(ctx, sc, "I'm not enabled in this conversation.")
		return
	}
	scope, ok := h.resolveScope(sc.UserID)
	if !ok {
		h.log.Warn("slack: slash command from unknown user — dropping",
			"slack_user_id", sc.UserID, "command", sc.Command)
		return
	}

	name, args := splitSlashCommand(sc.Command, sc.Text, h.slashPrefix())

	claims := &types.Claims{
		UserID: h.principalFor(ctx, sc.TeamID, sc.UserID),
		Scope:  scope,
	}
	claims.Roles = h.rolesFor(claims.UserID)

	// A slash command carries no channel_type, so the DM test is by id
	// prefix. Erring toward "shared" for anything unrecognised, for the
	// same asymmetry as everywhere else in this channel.
	shared := !slackChannelIsDM(sc.ChannelID)

	reply := h.commands.Dispatch(ctx, CommandRequest{
		Name:   name,
		Args:   args,
		Claims: claims,
		Session: SessionRef{
			Channel:   ChannelSlack,
			ChannelID: sc.ChannelID,
			UserID:    claims.UserID,
		},
		Shared: shared,
	})
	h.replyToCommand(ctx, sc, reply)
}

// replyToCommand answers the person who typed it, and only them.
//
// Falls back to a channel post when the ephemeral fails, which happens
// in a conversation the bot can post to but the user is not a member
// of. Losing the reply entirely would be worse than showing it to the
// room the command was already typed into.
func (h *SlackHandler) replyToCommand(ctx context.Context, sc slackSlashCommand, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := h.api.postEphemeral(ctx, sc.ChannelID, sc.UserID, "", text); err == nil {
		return
	} else {
		h.log.Debug("slack: ephemeral command reply failed; posting to the conversation",
			"channel", sc.ChannelID, "err", err)
	}
	h.sendText(ctx, sc.ChannelID, "", text)
}

// splitSlashCommand resolves the invoked command into a command name
// and its arguments.
//
// Two shapes, because both are legitimate. "/lobslaw new foo" is the
// umbrella form and takes its name from the text; "/status" is the
// flat form and is its own name. Distinguishing them on the prefix
// rather than on whether text is empty means "/lobslaw" alone resolves
// to an empty name — which Dispatch answers with a pointer to help,
// the right response to somebody who typed the prefix and stopped.
func splitSlashCommand(command, text, prefix string) (name, args string) {
	invoked := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(command), "/"))
	text = strings.TrimSpace(text)

	if invoked != strings.ToLower(prefix) {
		return invoked, text
	}
	first, rest, _ := strings.Cut(text, " ")
	return strings.ToLower(strings.TrimSpace(first)), strings.TrimSpace(rest)
}

func (h *SlackHandler) slashPrefix() string {
	if p := strings.TrimSpace(h.cfg.SlashPrefix); p != "" {
		return strings.TrimPrefix(p, "/")
	}
	return defaultSlashPrefix
}

// slackChannelIsDM reports whether a conversation id addresses a 1:1
// DM. Slack's ids are prefixed by kind: D is a DM, C a public channel,
// G a private one, and mpims arrive as C or G depending on age.
//
// Only used where no channel_type is available. Anything unrecognised
// counts as NOT a DM, so an id shape Slack adds later is treated as
// having an audience rather than as private.
func slackChannelIsDM(channelID string) bool {
	return strings.HasPrefix(channelID, "D")
}
