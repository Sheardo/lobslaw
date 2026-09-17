package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// DefaultTTL is the expiry window applied to notifications that
// don't carry an explicit ExpiresAt. Picked at 5 minutes per the
// design constraint: stale commitment-fire messages shouldn't
// reach the user hours after the bot recovered from an outage.
const DefaultTTL = 5 * time.Minute

// Urgency tags affect default TTL and may later affect routing
// (which sinks get the message in priority order). Today they're
// metadata only — the broadcast strategy delivers to all sinks
// regardless of urgency.
type Urgency string

const (
	UrgencyLow    Urgency = "low"
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Notification is one outbound message to a user. The payload is
// channel-agnostic plaintext; sinks render it however their
// channel demands.
type Notification struct {
	UserID            string
	Body              string
	Urgency           Urgency
	ExpiresAt         time.Time
	OriginatorChannel string
	OriginatorID      string
	Reason            string

	// SenderBot is the display name of the bot this came from, empty
	// for the assistant itself. Rendered as a prefix on the body.
	//
	// Attribution in the body rather than a separate channel identity
	// per bot: one Telegram token and one Slack app means a new bot
	// works the moment it is created, with no token to provision. The
	// trade is that every bot shares an avatar, and a label is what
	// stops "deploy finished" arriving with no idea who deployed.
	//
	// Stamped by the caller from turn identity, never from a tool
	// argument — a bot that could name its own sender could send you
	// something that looks like it came from another one.
	SenderBot string

	// RequestedBy is the principal who asked for this — the person
	// whose turn it was, when that is somebody other than the
	// recipient.
	//
	// Needed the moment more than one person can talk to the same
	// team. "Prepare a report and send it to James" is a perfectly
	// ordinary request, and without provenance James receives a
	// message from a bot with no way to tell whether he asked for it,
	// whether a colleague did, or whether it arrived unprompted. That
	// last reading is the dangerous one: anybody who can talk to a bot
	// could otherwise make it send messages that read as though the
	// system originated them.
	//
	// Stamped from turn identity like SenderBot, and for the same
	// reason: a requester the model could name is a requester the
	// model could invent.
	RequestedBy string
}

// Sink is one channel's delivery adapter. Each gateway channel
// (telegram, REST, future Slack/Matrix) registers a Sink at boot.
type Sink interface {
	ChannelType() string
	Deliver(ctx context.Context, address, body string) error
}

// SenderAware is an optional Sink capability: a channel that can show
// WHO a message is from gets told, and renders it however that channel
// does identity.
//
// Optional rather than part of Sink so existing sinks are unaffected.
// The distinction matters because attribution is otherwise a text
// prefix — "devops: deploy finished" — and a channel that can put the
// name in the message header would then show it twice.
type SenderAware interface {
	DeliverFrom(ctx context.Context, address, body, senderBot string) error
}

// deliverTo sends through a sink, giving it the sender if it can use
// one and a prefixed body if it cannot.
//
// Provenance goes in the BODY either way. A channel that can render a
// sender renders the bot — "DevOps" in the message header — and the
// requester is a different fact that no channel has a slot for.
func deliverTo(ctx context.Context, sink Sink, address, body, senderBot, requestedBy string) error {
	body = attributeRequest(body, requestedBy)
	if aware, ok := sink.(SenderAware); ok {
		return aware.DeliverFrom(ctx, address, body, senderBot)
	}
	return sink.Deliver(ctx, address, attributeSender(senderBot, body))
}

// describeRequester turns a requesting principal into a name worth
// printing, or empty when there is nothing to say.
//
// Empty when the requester IS the recipient — you asked for it, so
// being told you asked for it is noise — and when nothing was
// recorded, which is every self-generated notification: a routine
// firing at 3am was requested by nobody.
func (s *Service) describeRequester(ctx context.Context, requestedBy, recipient string) string {
	requestedBy = strings.TrimSpace(requestedBy)
	if requestedBy == "" {
		return ""
	}
	// Principals arrive as "user:sam"; the recipient id is bare.
	id := strings.TrimPrefix(requestedBy, "user:")
	if strings.EqualFold(id, recipient) {
		return ""
	}
	// A display name if we have one. Best-effort: a requester with no
	// prefs record is still worth naming by id, and failing the whole
	// notification over a missing display name would be absurd.
	if s.prefs != nil {
		if p, err := s.prefs.Get(ctx, id); err == nil && p != nil {
			if name := strings.TrimSpace(p.GetDisplayName()); name != "" {
				return name
			}
		}
	}
	return id
}

// PrefsLookup is the subset of memory.UserPrefsService the notify
// service needs. Interface so tests can substitute a fake.
type PrefsLookup interface {
	Get(ctx context.Context, userID string) (*lobslawv1.UserPreferences, error)
}

// Service dispatches Notifications across registered Sinks. One
// Service per node; multi-node clusters each run their own and
// the agent calls into whichever is local.
type Service struct {
	prefs  PrefsLookup
	logger *slog.Logger

	mu    sync.RWMutex
	sinks map[string]Sink
}

// NewService constructs a Service. prefs may be nil for test setups
// that pre-populate addresses out-of-band; production wires the
// memory.UserPrefsService here.
func NewService(prefs PrefsLookup, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		prefs:  prefs,
		logger: logger,
		sinks:  make(map[string]Sink),
	}
}

// RegisterSink installs a sink. Per channel type — registering a
// second sink for the same type replaces the first (the gateway
// channel layer guarantees one handler per channel anyway). Fails
// on empty ChannelType so a misconfigured handler crashes loudly
// at boot rather than silently dropping notifications.
func (s *Service) RegisterSink(sink Sink) error {
	if sink == nil {
		return errors.New("notify: nil sink")
	}
	t := strings.TrimSpace(sink.ChannelType())
	if t == "" {
		return errors.New("notify: sink has empty ChannelType")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinks[t] = sink
	return nil
}

// ErrExpired surfaces when a notification's ExpiresAt is in the
// past at the moment Send is called. Callers can branch on this
// for retry vs. drop logic.
var ErrExpired = errors.New("notify: notification expired before delivery")

// ErrUserUnbound surfaces when prefs has no record for the target
// user_id, OR has a record without channel addresses for any
// registered sink. Different from delivery failure (where a sink
// returned an error mid-deliver).
var ErrUserUnbound = errors.New("notify: user has no reachable channel addresses")

// Send dispatches n. Behaviour:
//
//   - Expired ⇒ return ErrExpired without touching any sink.
//   - OriginatorChannel set ⇒ deliver only on that channel using
//     the user's bound address for that channel type.
//   - OriginatorChannel empty ⇒ broadcast: deliver on every
//     channel-address pair the user has bound that has a
//     registered sink.
//
// Per-sink failures are logged and continue (one bad channel
// shouldn't block the rest). Returns nil iff at least one sink
// successfully delivered, OR ErrUserUnbound when no sink could
// be matched, OR ErrExpired.
func (s *Service) Send(ctx context.Context, n Notification) error {
	if strings.TrimSpace(n.UserID) == "" {
		return errors.New("notify: user_id required")
	}
	if strings.TrimSpace(n.Body) == "" {
		return errors.New("notify: body required")
	}
	if n.ExpiresAt.IsZero() {
		n.ExpiresAt = time.Now().Add(DefaultTTL)
	}
	if time.Now().After(n.ExpiresAt) {
		s.logger.Warn("notify: dropping expired notification",
			"user", n.UserID, "expires_at", n.ExpiresAt, "reason", n.Reason)
		return ErrExpired
	}

	prefs, err := s.lookupPrefs(ctx, n.UserID)
	if err != nil {
		return err
	}

	// Resolve the requester to something a person recognises, and drop
	// it when they are the recipient.
	n.RequestedBy = s.describeRequester(ctx, n.RequestedBy, n.UserID)

	if n.OriginatorChannel != "" {
		return s.deliverOriginator(ctx, n, prefs)
	}
	return s.broadcast(ctx, n, prefs)
}

func (s *Service) lookupPrefs(ctx context.Context, userID string) (*lobslawv1.UserPreferences, error) {
	if s.prefs == nil {
		return nil, errors.New("notify: prefs lookup not wired")
	}
	prefs, err := s.prefs.Get(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("notify: lookup prefs for %s: %w", userID, err)
	}
	return prefs, nil
}

// deliverOriginator handles the inbound-reply path: deliver only
// on the originating channel using the address bound for that
// channel type. Failures here are real errors (the user is
// expecting a reply on this exact channel).
func (s *Service) deliverOriginator(ctx context.Context, n Notification, prefs *lobslawv1.UserPreferences) error {
	addr := findChannelAddress(prefs, n.OriginatorChannel)
	if addr == "" {
		// Originator delivery falls back to OriginatorID — the
		// channel-level identifier from the inbound message.
		// This handles the case where prefs hasn't been
		// populated yet but we still have a live channel context.
		addr = n.OriginatorID
	}
	if addr == "" {
		return fmt.Errorf("%w (originator channel %q has no bound address)",
			ErrUserUnbound, n.OriginatorChannel)
	}
	sink := s.sinkFor(n.OriginatorChannel)
	if sink == nil {
		return fmt.Errorf("notify: no sink registered for channel %q", n.OriginatorChannel)
	}
	if err := deliverTo(ctx, sink, addr, n.Body, n.SenderBot, n.RequestedBy); err != nil {
		return err
	}
	s.logger.Info("notify: delivered",
		"user", n.UserID, "channel", n.OriginatorChannel,
		"from", n.SenderBot, "requested_by", n.RequestedBy)
	return nil
}

// broadcast handles the self-generated path: deliver on every
// (channel, address) pair in prefs that has a registered sink.
// Continues past per-sink errors — partial delivery is better
// than zero delivery. Returns ErrUserUnbound iff no sinks
// matched any of the user's bindings.
func (s *Service) broadcast(ctx context.Context, n Notification, prefs *lobslawv1.UserPreferences) error {
	if prefs == nil || len(prefs.Channels) == 0 {
		return ErrUserUnbound
	}
	delivered := 0
	for _, c := range prefs.Channels {
		sink := s.sinkFor(c.Type)
		if sink == nil {
			s.logger.Debug("notify: no sink for channel type; skipping",
				"user", n.UserID, "channel", c.Type)
			continue
		}
		if err := deliverTo(ctx, sink, c.Address, n.Body, n.SenderBot, n.RequestedBy); err != nil {
			s.logger.Warn("notify: sink delivery failed",
				"user", n.UserID, "channel", c.Type, "err", err)
			continue
		}
		// Success was silent, so "did it actually send?" could only be
		// answered by asking the person whether their phone buzzed.
		// With several channels bound and some of them failing, the
		// log showed only the failures and read like total failure.
		s.logger.Info("notify: delivered",
			"user", n.UserID, "channel", c.Type,
			"from", n.SenderBot, "requested_by", n.RequestedBy)
		delivered++
	}
	if delivered == 0 {
		return ErrUserUnbound
	}
	return nil
}

func (s *Service) sinkFor(channelType string) Sink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sinks[channelType]
}

// findChannelAddress returns the prefs address bound for the given
// channel type, or "" when not found.
func findChannelAddress(prefs *lobslawv1.UserPreferences, channelType string) string {
	if prefs == nil {
		return ""
	}
	for _, c := range prefs.Channels {
		if c.Type == channelType {
			return c.Address
		}
	}
	return ""
}

// attributeSender prefixes a bot's messages with its name.
//
// Sinks render channel-agnostic plaintext, so this is the one place a
// team of bots becomes legible on a single channel identity. Done in
// the service rather than in each sink so a new channel inherits it
// instead of forgetting it.
func attributeSender(sender, body string) string {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return body
	}
	return sender + ": " + body
}

// attributeRequest appends who asked, when that is somebody other than
// the person reading it.
//
// Appended rather than prefixed: the message is the point and the
// provenance is the footnote. Omitted entirely when the requester is
// the recipient, because "asked by you" on something you asked for is
// noise that trains people to stop reading the line that matters.
func attributeRequest(body, requestedBy string) string {
	requestedBy = strings.TrimSpace(requestedBy)
	if requestedBy == "" {
		return body
	}
	return body + "\n\n(" + requestedBy + " asked me to send you this)"
}
