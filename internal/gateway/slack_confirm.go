package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/policy"
)

// sendConfirmationBlocks renders a paused turn as a Block Kit message.
//
// The Telegram equivalent's structure carries over unchanged, including
// the part that matters most: the paused turn rides on the prompt
// record rather than a map on this handler, so an approval survives a
// restart and can be answered on a different node.
//
// action_id carries "prompt:<verb>:<id>", which is how the tap is
// routed back without a side table. Slack caps action_id at 255 bytes;
// a verb plus a ULID is nowhere near it.
func (h *SlackHandler) sendConfirmationBlocks(ctx context.Context, channel, thread string, req compute.ProcessMessageRequest, resp *compute.ProcessMessageResponse, session SessionRef) {
	ttl := h.cfg.ConfirmationTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	p, err := h.cfg.Prompts.Create(NewPrompt{
		TurnID:       req.TurnID,
		SessionID:    session.ChannelID,
		Reason:       resp.ConfirmationReason,
		Channel:      ChannelSlack,
		ChannelID:    channel,
		TTL:          ttl,
		Action:       resp.ConfirmationAction,
		Resource:     resp.ConfirmationResource,
		Continuation: &Continuation{Request: req, Messages: resp.Messages},
		// Who may answer, captured from the turn rather than read off
		// the tap. In a Slack channel this is load-bearing in a way it
		// is not in a Telegram DM: everyone in the room can see the
		// buttons, so without it the person who approves is whoever
		// clicks first.
		RaisedFor: session.UserID,
	})
	if err != nil {
		h.log.Error("slack: prompt registration failed", "err", err)
		h.sendText(ctx, channel, thread, "Confirmation required: "+resp.ConfirmationReason)
		return
	}

	buttons := []any{button("Approve", "prompt:approve:"+p.ID, "primary")}

	// "for this conversation" and "always" are offered only when a
	// policy rule asked the question. A budget confirmation is about
	// spend, so there is no operation to remember and a button that
	// silenced future budget warnings would be actively harmful.
	if resp.ConfirmationAction != "" && resp.ConfirmationResource != "" {
		subject := grantSubject(req.Claims)
		h.pendingScopeMu.Lock()
		h.pendingScope[p.ID] = scopedOperation{
			action:   resp.ConfirmationAction,
			resource: resp.ConfirmationResource,
			subject:  subject,
		}
		h.pendingScopeMu.Unlock()

		buttons = append(buttons, button("Approve here", "prompt:approve-session:"+p.ID, ""))
		if subject != "" && h.cfg.ApprovalRules != nil {
			buttons = append(buttons, button("Always allow", "prompt:approve-always:"+p.ID, ""))
		}
	}
	buttons = append(buttons, button("Deny", "prompt:deny:"+p.ID, "danger"))

	blocks := []any{
		map[string]any{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": "*Confirmation required*\n" + resp.ConfirmationReason,
			},
		},
		map[string]any{"type": "actions", "elements": buttons},
	}

	// The plain text is the notification body and the fallback for any
	// client that cannot render blocks. Without it the prompt arrives
	// on a phone as an empty message.
	fallback := "Confirmation required: " + resp.ConfirmationReason
	if err := h.api.postBlocks(ctx, channel, thread, fallback, blocks); err != nil {
		h.log.Error("slack: confirmation post failed", "err", err)
		h.sendText(ctx, channel, thread, fallback)
	}
}

func button(label, actionID, style string) map[string]any {
	b := map[string]any{
		"type":      "button",
		"text":      map[string]any{"type": "plain_text", "text": label},
		"action_id": actionID,
		"value":     actionID,
	}
	if style != "" {
		b["style"] = style
	}
	return b
}

// handleInteraction resolves a prompt from a Block Kit button tap.
//
// Mirrors the Telegram callback path, verb for verb, so the two
// channels cannot drift on what "approve for this conversation" means.
func (h *SlackHandler) handleInteraction(ctx context.Context, in slackInteraction) {
	if in.Type != "block_actions" || len(in.Actions) == 0 {
		h.log.Debug("slack: unhandled interaction", "type", in.Type)
		return
	}
	channel := in.Channel.ID
	thread := in.Message.ThreadTS
	if thread == "" {
		thread = in.Message.TS
	}
	if !h.isAllowedChannel(channel) {
		h.log.Info("slack: interaction from a channel not in allowed_channels — dropping",
			"channel", channel, "user", in.User.ID)
		return
	}

	parts := strings.SplitN(in.Actions[0].ActionID, ":", 3)
	if len(parts) != 3 || parts[0] != "prompt" {
		h.log.Debug("slack: unhandled action_id shape", "action_id", in.Actions[0].ActionID)
		return
	}
	verb, promptID := parts[1], parts[2]

	if h.cfg.Prompts == nil {
		h.log.Warn("slack: interaction arrived but no prompt registry configured")
		return
	}
	teamID := in.Team.ID
	if teamID == "" {
		teamID = in.User.TeamID
	}
	if !h.mayResolve(ctx, promptID, teamID, in.User.ID, channel, thread) {
		return
	}

	// Read before resolving: Resolve is a CAS that can lose to another
	// node, and the loser must not consume the turn it did not win.
	prompt, getErr := h.cfg.Prompts.Get(promptID)

	// The conversation a grant is scoped to comes from the PROMPT, not
	// from the tap.
	//
	// Reconstructing it from the button was wrong, and quietly: a
	// top-level channel message has no thread_ts, so the turn is scoped
	// to "C123" — but the confirmation is posted INTO a thread, so the
	// tap comes back carrying one and rebuilt "C123/1.1". The grant
	// landed under a conversation the turn was never in, and "approve
	// here" silently asked again next time.
	//
	// It is also the same argument the subject already follows: a
	// callback is attacker-shaped input, the turn that raised the
	// question is not.
	grantSession := ""
	if getErr == nil && prompt != nil {
		grantSession = prompt.SessionID
	}

	// Whatever the verb, this prompt is finished after this tap, so its
	// remembered operation goes. The grant helpers below take it first
	// when they need it; this drains the rest — a plain "approve", a
	// "deny", or a grant that could not be recorded. Without it the map
	// only ever grows, keyed by prompts that will never be tapped again.
	defer h.takePendingScope(promptID)

	var decision PromptDecision
	var scope PromptScope
	var reply string
	switch verb {
	case "approve":
		decision, scope = PromptApproved, PromptScopeOnce
		reply = "Approved."
	case "approve-session":
		decision, scope = PromptApproved, PromptScopeSession
		reply = "Approved — I won't ask again for this in this conversation."
		// Recorded before Resolve so the resumed turn already sees the
		// grant; resolving first lets the resume race it and prompt a
		// second time for the same operation.
		if !h.grantForSession(ctx, promptID, grantSession) {
			decision, scope = PromptApproved, PromptScopeOnce
			reply = "Approved."
		}
	case "approve-always":
		decision, scope = PromptApproved, PromptScopeAlways
		reply = "Approved — I won't ask about this again. Revoke it with `lobslaw policy revoke-approvals`."
		if !h.grantAlways(ctx, promptID) {
			decision, scope = PromptApproved, PromptScopeOnce
			reply = "Approved."
		}
	case "deny":
		decision, scope = PromptDenied, PromptScopeOnce
		reply = "Denied."
	default:
		h.log.Debug("slack: unknown prompt verb", "verb", verb)
		return
	}

	if err := h.cfg.Prompts.Resolve(promptID, decision, scope); err != nil {
		switch {
		case errors.Is(err, ErrPromptNotFound):
			reply = "That prompt no longer exists."
		case errors.Is(err, ErrPromptResolved):
			reply = "That prompt was already resolved."
		default:
			h.log.Error("slack: resolve failed", "err", err, "id", promptID)
			reply = "Couldn't process the response."
		}
		h.sendText(ctx, channel, thread, reply)
		return
	}
	h.sendText(ctx, channel, thread, reply)

	if decision == PromptDenied {
		return
	}
	if getErr != nil || prompt == nil || prompt.Continuation == nil {
		h.log.Warn("slack: approve with no continuation", "prompt_id", promptID, "err", getErr)
		h.sendText(ctx, channel, thread, "I've lost track of that turn — send it again.")
		return
	}
	h.resumeAfterApproval(ctx, prompt, thread)
}

// mayResolve reports whether this tap may answer this prompt.
//
// The prompt id is unguessable, but unguessable is not authorised: the
// buttons are rendered into a channel where everyone can see and click
// them. This is the Slack form of the fix in #127, and it matters more
// here — a Telegram confirmation usually lands in a DM, a Slack one
// routinely lands in a room.
//
// Fails CLOSED. A prompt with no recorded audience is one this node
// cannot attribute an answer to.
func (h *SlackHandler) mayResolve(ctx context.Context, promptID, teamID, userID, channel, thread string) bool {
	p, err := h.cfg.Prompts.Get(promptID)
	if err != nil || p == nil {
		// Expired or reaped rather than an authorisation failure;
		// Resolve reports it properly a moment later.
		return true
	}
	if p.RaisedFor == "" {
		h.log.Warn("slack: refusing a tap on a prompt with no recorded audience", "prompt", promptID)
		h.sendText(ctx, channel, thread, "This confirmation cannot be attributed to anyone; it was not applied.")
		return false
	}
	if userID == "" {
		h.log.Warn("slack: refusing an unattributed tap", "prompt", promptID)
		return false
	}
	if !h.isAudience(ctx, teamID, userID, p.RaisedFor) {
		// Logged with both principals: somebody clicking a colleague's
		// confirmation is worth seeing, and is indistinguishable from
		// an attack otherwise.
		h.log.Warn("slack: refusing a tap from somebody the question was not asked of",
			"prompt", promptID,
			"tapped_by", h.principalFor(ctx, teamID, userID),
			"raised_for", p.RaisedFor)
		h.sendText(ctx, channel, thread, "That confirmation was not for you.")
		return false
	}
	return true
}

// isAudience reports whether this user is who the question was asked
// of, comparing principal to principal so it survives an identity
// rebind. The raw channel-derived id is accepted too, for a prompt
// raised before any binding existed.
func (h *SlackHandler) isAudience(ctx context.Context, teamID, userID, raisedFor string) bool {
	if raisedFor == "" || userID == "" {
		return false
	}
	if h.principalFor(ctx, teamID, userID) == raisedFor {
		return true
	}
	return slackUserIdentity(teamID, userID) == raisedFor
}

// grantForSession records "approved for the rest of this conversation".
// Reports whether a grant was actually recorded, so the reply does not
// promise something that did not happen.
func (h *SlackHandler) grantForSession(ctx context.Context, promptID, convID string) bool {
	// Taken before the store is checked so the entry is consumed even
	// when there is nowhere to record the grant. A pending scope that
	// outlives its prompt is a slow leak and, worse, something a later
	// tap on a recycled id could pick up.
	op, ok := h.takePendingScope(promptID)
	if !ok || h.cfg.Approvals == nil {
		return false
	}
	if convID == "" {
		// No conversation to scope to. Refusing means the caller
		// narrows to a one-shot approval, which is the safe direction:
		// a grant scoped to nothing is either scoped to everything or
		// findable by nobody, and both are wrong.
		h.log.Warn("slack: session grant has no conversation; narrowing to once",
			"prompt", promptID)
		return false
	}
	grantCtx := compute.WithTurnIdentity(ctx, compute.TurnIdentity{
		Channel:   ChannelSlack,
		ChannelID: convID,
	})
	if !h.cfg.Approvals.Grant(grantCtx, op.action, op.resource) {
		h.log.Warn("slack: could not record session approval",
			"action", op.action, "resource", op.resource)
		return false
	}
	h.log.Info("slack: approved for this conversation",
		"action", op.action, "resource", op.resource, "conversation", convID)
	return true
}

// grantAlways mints the revocable policy rule behind "always".
func (h *SlackHandler) grantAlways(ctx context.Context, promptID string) bool {
	// Taken first, for the same reason as grantForSession.
	op, ok := h.takePendingScope(promptID)
	if !ok || op.subject == "" || h.cfg.ApprovalRules == nil {
		return false
	}
	rule, err := h.cfg.ApprovalRules.Mint(ctx, policy.MintRequest{
		PromptID: promptID,
		Subject:  op.subject,
		Action:   op.action,
		Resource: op.resource,
	})
	if err != nil {
		h.log.Warn("slack: could not mint a permanent approval",
			"action", op.action, "resource", op.resource, "err", err)
		return false
	}
	h.log.Info("slack: permanent approval recorded",
		"rule_id", rule.Id, "subject", op.subject,
		"action", op.action, "resource", op.resource)
	return true
}

func (h *SlackHandler) takePendingScope(promptID string) (scopedOperation, bool) {
	h.pendingScopeMu.Lock()
	defer h.pendingScopeMu.Unlock()
	op, ok := h.pendingScope[promptID]
	delete(h.pendingScope, promptID)
	return op, ok
}

// resumeAfterApproval re-enters the agent loop with a relaxed budget
// and delivers the result back to the thread the question was asked in.
func (h *SlackHandler) resumeAfterApproval(ctx context.Context, p *Prompt, thread string) {
	cont := p.Continuation
	channel := p.ChannelID
	session := SessionRef{Channel: ChannelSlack, ChannelID: p.SessionID}

	// Tools stay nil: fillDefaults repopulates them from the resuming
	// node's own registry, so a serialised definition cannot outlive
	// the redeploy that changed it.
	cont.Request.TurnID = p.TurnID
	cont.Request.Channel = ChannelSlack
	cont.Request.ChannelID = p.SessionID

	cont.Request.Budget.Relax()
	resp, err := h.agent.ResumeFromConfirmation(ctx, cont.Request, cont.Messages)
	if err != nil {
		h.log.Error("slack: resume failed", "turn_id", cont.Request.TurnID, "err", err)
		h.sendText(ctx, channel, thread, classifyAgentError(err))
		return
	}

	if newTurn := newTurnMessages(resp.Messages, resp.TurnStartIndex); len(newTurn) > 0 {
		h.conv.Append(ctx, session, cont.Request.TurnID, newTurn)
	}

	switch {
	case resp.NeedsConfirmation:
		h.sendConfirmationBlocks(ctx, channel, thread, cont.Request, resp, session)
	case resp.Reply == "":
		h.sendText(ctx, channel, thread, "(empty reply)")
	default:
		h.sendText(ctx, channel, thread, resp.Reply)
	}
	// The resumed leg is where a confirmed generation actually
	// produces its file, so this is the delivery point that matters
	// for anything gated behind an approval.
	h.SendAttachments(ctx, channel, thread, resp.Attachments, h.cfg.ArtifactOpener)
}
