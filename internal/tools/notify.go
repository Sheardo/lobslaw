package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/notify"
	"github.com/jmylchreest/lobslaw/internal/turn"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Notifier is the channel-agnostic notification dispatch interface
// the `notify` builtin calls into. internal/notify.Service satisfies
// it; tests substitute a fake recorder.
type Notifier interface {
	Send(ctx context.Context, n notify.Notification) error
}

// NotifyConfig wires the notify builtin. Nil Service skips
// registration — deployments without any gateway channel running a
// Sink won't see this tool. Operators with at least one channel
// configured (Telegram, REST, future Slack) get it always.
type NotifyConfig struct {
	Service Notifier
}

// RegisterNotifyBuiltins installs the channel-agnostic `notify`
// builtin. The agent passes a canonical user_id (resolved by the
// channel layer at inbound time, or by the commitment record's
// CreatedFor at fire time); the notify service routes to whichever
// channels that user has bound in their preferences.
func RegisterNotifyBuiltins(b *Builtins, cfg NotifyConfig) error {
	if cfg.Service == nil {
		return errors.New("notify builtins: notify.Service required")
	}
	return b.Register("notify", newNotifyHandler(cfg.Service))
}

// NotifyToolDefs returns the LLM-facing tool registration for
// `notify`. The shape replaces the channel-specific predecessors
// (notify_telegram et al) — operators who want per-channel routing
// get it via the user's preferences bucket, not a per-builtin tool.
func NotifyToolDefs() []*types.ToolDef {
	return []*types.ToolDef{
		{
			Name:        "notify",
			Path:        compute.BuiltinScheme + "notify",
			Description: "Send a proactive message to a user across every channel they're subscribed to. Use ONLY for proactive messaging — commitment fires, scheduled-task results, async research completions, follow-ups on turns out-of-band. For normal in-chat replies, return your reply text directly (the gateway delivers it automatically; calling notify duplicates the message). Pass user_id (canonical user identifier — usually \"owner\" for solo deployments) to reach someone other than the person whose turn this is; omit it to reach the caller. text is the message body. Optional ttl_seconds (default 300) caps how long the message is allowed to wait before delivery; expired messages drop silently with an audit log.",
			ParametersSchema: []byte(`{
				"type": "object",
				"properties": {
					"user_id":     {"type": "string", "description": "Canonical user id. Omit to notify the caller of this turn."},
					"text":        {"type": "string", "description": "Message body."},
					"ttl_seconds": {"type": "integer", "description": "Expiry in seconds. Default 300 (5 min). Past this, the message is dropped."}
				},
				"required": ["text"],
				"additionalProperties": false
			}`),
			RiskTier: types.RiskCommunicating,
		},
	}
}

func newNotifyHandler(svc Notifier) compute.BuiltinFunc {
	return func(ctx context.Context, args map[string]string) ([]byte, int, error) {
		identity, _ := turn.IdentityFrom(ctx)
		// An explicit user_id is a routing argument the model may
		// legitimately set — "tell alice her build finished". Falling
		// back to the turn's caller is the common case. The fallback
		// comes from the context, never from the args: this decides
		// whose devices ring, and the model does not get to pick.
		userID := strings.TrimSpace(args["user_id"])
		if userID == "" {
			userID = identity.UserID
		}
		if userID == "" {
			return nil, 2, errors.New("notify: user_id is required (this turn has no caller identity to fall back on)")
		}
		text := args["text"]
		if strings.TrimSpace(text) == "" {
			return nil, 2, errors.New("notify: text is required")
		}

		n := notify.Notification{
			UserID: userID,
			Body:   text,
			// Only when a person could actually be waiting there.
			// A synthetic channel has no sink and never will, and
			// naming it here turns "tell the user however they asked
			// to be told" into "reply into a transcript" — which
			// fails, and takes the notification with it.
			OriginatorChannel: humanChannel(identity),
			OriginatorID:      humanChannelID(identity),
			// From the turn, not from args. A bot that could name its
			// own sender could send you something that looks like it
			// came from a different one.
			SenderBot: senderLabel(identity),
			// Who asked. From the turn's principal, never from args:
			// a requester the model can name is a requester it can
			// invent, and the whole value of the line is that it is
			// true.
			//
			// Only when somebody else is being messaged — the service
			// drops it when the requester is the recipient, so the
			// common "tell me when it's done" stays clean.
			RequestedBy: requesterLabel(identity),
		}
		if raw := strings.TrimSpace(args["ttl_seconds"]); raw != "" {
			secs, err := parseTTL(raw)
			if err != nil {
				return nil, 2, fmt.Errorf("notify: ttl_seconds: %w", err)
			}
			n.ExpiresAt = time.Now().Add(secs)
		}

		if err := svc.Send(ctx, n); err != nil {
			if errors.Is(err, notify.ErrExpired) {
				return nil, 1, fmt.Errorf("notify: dropped (expired before delivery)")
			}
			if errors.Is(err, notify.ErrUserUnbound) {
				return nil, 1, fmt.Errorf("notify: user %q has no reachable channel addresses; ask the operator to bind one", userID)
			}
			return nil, 1, fmt.Errorf("notify: %w", err)
		}
		out, _ := json.Marshal(map[string]any{
			"user_id":   userID,
			"delivered": true,
		})
		return out, 0, nil
	}
}

func parseTTL(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw + "s")
	if err == nil {
		return d, nil
	}
	d, err = time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("must be seconds (\"30\") or duration (\"30s\", \"5m\"): %w", err)
	}
	return d, nil
}

// senderLabel names the bot a notification came from, empty for the
// assistant itself.
//
// The bare id rather than a display name: the display name lives on
// the bot record, and reaching the registry from inside a tool handler
// to render a label would put a raft read on the path of every
// proactive message. An id is what the operator typed and is what the
// GUI shows beside the queue.
func senderLabel(identity turn.Identity) string {
	if !identity.IsBot() {
		return ""
	}
	return identity.BotID
}

// humanChannel is the turn's channel, or empty when nobody is waiting
// there. Empty is what makes notify broadcast to the user's bound
// addresses instead of trying to reply in place.
func humanChannel(identity turn.Identity) string {
	if !identity.IsHumanChannel() {
		return ""
	}
	return identity.Channel
}

// humanChannelID goes with it: the fallback address notify uses when
// prefs hold nothing for the originating channel is only meaningful
// if that channel was real.
func humanChannelID(identity turn.Identity) string {
	if !identity.IsHumanChannel() {
		return ""
	}
	return identity.ChannelID
}

// requesterLabel is the principal who asked for a notification.
//
// A bot's own principal is not a requester: when DevOps notifies as
// part of working an inbox item, nobody asked it in the sense that
// matters here, and "bot:devops asked me to send you this" beside
// "DevOps" in the header says the same thing twice. The person who
// put the work in the queue is the interesting answer, and that is
// not available at this layer.
func requesterLabel(identity turn.Identity) string {
	// A requester carried across a delegation hop wins: it is the
	// person who actually asked, and the bot running this turn is only
	// the one doing it.
	if r := strings.TrimSpace(identity.RequestedBy); r != "" {
		return r
	}
	// The PRINCIPAL decides, not Identity.IsBot().
	//
	// IsBot() is `BotID != ""`, and since channel turns resolve to the
	// default team's coordinator, every turn now has a BotID —
	// including one a person is driving. Testing it here discarded the
	// requester in exactly the case where there was one: Sam asks the
	// coordinator to send James a report, the turn runs as the
	// coordinator, and Sam disappeared.
	//
	// A turn whose principal is a bot really has no requester: that is
	// a bot acting on its own, and anything it was asked to do arrives
	// through RequestedBy above.
	if identity.Principal.IsBot() {
		return ""
	}
	if p := identity.Principal.String(); p != "" {
		return p
	}
	return identity.UserID
}
