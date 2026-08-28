package gateway

import (
	"context"
	"strings"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// handleMessage runs one Slack message as a turn.
//
// Deliberately the same sequence as the Telegram handler's: authorise,
// serialise on the conversation, load the transcript, run the agent
// under the responsiveness guards, persist the whole thread, reply.
// Where the two differ it is because Slack differs — the reply goes to
// a thread, and the conversation may have an audience.
func (h *SlackHandler) handleMessage(ctx context.Context, teamID string, ev slackEvent) {
	if !h.isAllowedChannel(ev.Channel) {
		// Deliberately silent to the room. A bot that announces its own
		// refusal in a channel it was not permitted to speak in has
		// just spoken in that channel.
		h.log.Info("slack: channel not in allowed_channels — dropping",
			"channel", ev.Channel, "user", ev.User)
		return
	}

	scope, ok := h.resolveScope(ev.User)
	if !ok {
		h.log.Warn("slack: unknown user, UnknownUserScope empty — dropping",
			"slack_user_id", ev.User, "channel", ev.Channel)
		return
	}

	budget, err := compute.NewTurnBudget(h.cfg.DefaultBudget)
	if err != nil {
		h.log.Error("slack: budget init failed", "err", err)
		return
	}

	claims := &types.Claims{
		UserID: h.principalFor(ctx, teamID, ev.User),
		Scope:  scope,
	}
	claims.Roles = h.rolesFor(claims.UserID)

	convID := conversationID(ev)
	thread := replyThread(ev)
	shared := isSharedSlackConversation(ev)
	turnID := "slack-" + ev.Channel + "-" + ev.TS

	h.log.Debug("slack: message received",
		"turn_id", turnID,
		"slack_user_id", ev.User,
		"channel", ev.Channel,
		"conversation", convID,
		"shared", shared,
		"scope", scope)

	sessionRef := SessionRef{
		Channel:   ChannelSlack,
		ChannelID: convID,
		UserID:    claims.UserID,
	}

	lease, disposition := h.gate.Acquire(ctx, cacheKey(sessionRef), turnID, ev.Text)
	switch disposition {
	case Folded:
		h.log.Debug("slack: message folded into an in-flight turn",
			"turn_id", turnID, "conversation", convID)
		return
	case Dropped:
		h.log.Info("slack: message dropped by queue policy",
			"turn_id", turnID, "conversation", convID, "mode", h.gate.Mode())
		if h.gate.Mode() == QueueOff {
			h.sendText(ctx, ev.Channel, thread,
				"Still working on your previous message — send that again once I've replied.")
		}
		return
	}
	defer lease.Release()

	prior := h.conv.Load(ctx, sessionRef)

	// lease.Batch is this message plus anything folded into it while
	// it waited; using ev.Text alone would drop the fragments the gate
	// promised to answer.
	body := strings.Join(lease.Batch, "\n")
	if body == "" {
		body = ev.Text
	}
	body = stripBotMention(body, h.botUserID)

	agentReq := compute.ProcessMessageRequest{
		Message:             body,
		Claims:              claims,
		TurnID:              turnID,
		Budget:              budget,
		ConversationHistory: prior.Messages,
		ConversationSummary: prior.Summary,
		Channel:             ChannelSlack,
		ChannelID:           convID,
		SharedConversation:  shared,
	}

	turnCtx, cleanup := h.startResponsivenessGuards(ctx, ev.Channel, thread)
	defer cleanup()

	resp, err := h.agent.RunToolCallLoop(turnCtx, agentReq)
	if err != nil {
		h.log.Error("slack: agent error", "turn_id", turnID, "err", err)
		h.sendText(ctx, ev.Channel, thread, classifyAgentError(err))
		return
	}

	if newTurn := newTurnMessages(resp.Messages, resp.TurnStartIndex); len(newTurn) > 0 {
		h.conv.Append(ctx, sessionRef, turnID, newTurn)
	}

	switch {
	case resp.NeedsConfirmation:
		if h.cfg.Prompts != nil {
			h.sendConfirmationBlocks(ctx, ev.Channel, thread, agentReq, resp, sessionRef)
			return
		}
		// No registry wired — render the reason as text. The turn
		// pauses safely either way; it just cannot be resumed by a tap.
		h.sendText(ctx, ev.Channel, thread, "Confirmation required: "+resp.ConfirmationReason)
	case resp.Reply == "":
		h.sendText(ctx, ev.Channel, thread, "(empty reply)")
	default:
		h.sendText(ctx, ev.Channel, thread, h.cfg.Notices.Append(ctx,
			ChannelSlack, convID, "scope:"+scope+":"+claims.UserID, resp.Reply, ev.User))
	}
}

// stripBotMention removes the leading "<@U…>" an app_mention carries.
//
// Slack delivers the raw mention markup in the text, so without this
// every turn in a channel begins with a token the model has to guess
// the meaning of, and a message that is ONLY a mention arrives as
// non-empty text with no content.
func stripBotMention(text, botUserID string) string {
	if botUserID == "" {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "<@"+botUserID+">", ""))
}

// sendText posts a reply, threading it when thread is set. Errors are
// logged rather than returned: the turn has already happened, and
// there is nowhere left to report a delivery failure to.
func (h *SlackHandler) sendText(ctx context.Context, channel, thread, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := h.api.postMessage(ctx, channel, thread, text); err != nil {
		h.log.Error("slack: postMessage failed",
			"channel", channel, "thread", thread, "err", err)
	}
}
