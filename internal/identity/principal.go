// Package identity resolves the per-channel user ids lobslaw receives
// into the cluster-wide principals that ownership and visibility are
// decided against.
//
// The two are not the same thing, and conflating them is what makes
// scoping either leaky or useless. A channel id is whatever that
// channel happens to call someone — "tg-@alice" from Telegram, a JWT
// subject over REST, a webhook's configured scope. The same human
// arrives under a different one on every channel, so scoping directly
// on the channel id makes one person several: they stop finding their
// own history the moment they switch app. Scoping on nothing at all
// makes everyone one person, which is the bug this exists to prevent.
//
// So the operator declares the mapping, and everything downstream
// decides against the result.
package identity

import (
	"sort"
	"strings"
)

// Principal is a canonical, cluster-wide identity: the thing a record
// can be owned by and a request can be authorised as.
//
// Rendered as "<kind>:<id>" so the kind is legible wherever a
// principal is stored or logged, and so two kinds can never collide on
// a bare id. Two kinds exist today:
//
//	user:alice                 a person
//	chat:telegram:-1001234     a conversation, owned collectively
//
// The second matters because ownership is not always individual. A
// Telegram group chat's transcript belongs to the chat, not to whoever
// happened to speak first, and modelling that as a principal is what
// keeps group sharing from being a special case in every reader.
type Principal string

// Kind prefixes. Exported because callers construct principals for
// records they are about to write.
const (
	KindUser = "user"
	KindChat = "chat"
)

// User returns the principal for a person. An empty id yields the
// empty Principal rather than "user:", so callers can test for absence
// without knowing the encoding.
func User(id string) Principal {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return Principal(KindUser + ":" + id)
}

// Chat returns the principal for a conversation that belongs to its
// participants collectively rather than to any one of them.
func Chat(channel, channelID string) Principal {
	channel, channelID = strings.TrimSpace(channel), strings.TrimSpace(channelID)
	if channel == "" || channelID == "" {
		return ""
	}
	return Principal(KindChat + ":" + channel + ":" + channelID)
}

// String renders the principal for storage and logs.
func (p Principal) String() string { return string(p) }

// IsZero reports the absence of an identity — an anonymous turn, or a
// record written before ownership existed.
func (p Principal) IsZero() bool { return p == "" }

// Resolver maps the per-channel user ids that arrive on a turn to
// canonical principals.
//
// Unmapped ids are NOT rejected. A deployment that never configures an
// alias still gets correct behaviour — every channel id becomes its
// own principal, which is exactly today's semantics — and only a
// deployment that wants two channel ids treated as one person needs to
// say so. Requiring registration up front would mean a new user cannot
// talk to the bot until an operator edits a file.
type Resolver struct {
	aliases map[string]Principal
}

// NewResolver builds a resolver from an operator's alias map: channel
// user id → canonical principal id.
//
//	[identity.aliases]
//	"tg-@alice"         = "alice"
//	"alice@example.com" = "alice"
//
// Values are bare ids, not "user:alice" — the kind is this package's
// business, and letting a config file choose it would let a typo mint
// a principal kind nothing else understands. Keys are matched
// case-insensitively: channels differ on whether they preserve the
// case of a handle, and "TG-@Alice" being a different person from
// "tg-@alice" is never what an operator meant.
func NewResolver(aliases map[string]string) *Resolver {
	r := &Resolver{aliases: make(map[string]Principal, len(aliases))}
	for from, to := range aliases {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" {
			continue
		}
		r.aliases[strings.ToLower(from)] = User(to)
	}
	return r
}

// Resolve returns the canonical principal for a channel user id.
// Unmapped ids become their own principal. An empty id yields the zero
// Principal — an anonymous turn owns nothing and can read nothing
// owned by anyone else.
//
// A nil Resolver resolves everything to itself, so a caller that has
// no configuration is not forced to construct one.
func (r *Resolver) Resolve(userID string) Principal {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	if r != nil {
		if p, ok := r.aliases[strings.ToLower(userID)]; ok {
			return p
		}
	}
	return User(userID)
}

// Aliases returns the configured mappings, sorted, for logging at boot.
// Operators need to see what the node believes about identity: a typo
// in an alias key fails silently and open — the id simply resolves to
// itself and that person quietly stops seeing their own history.
func (r *Resolver) Aliases() []string {
	if r == nil || len(r.aliases) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.aliases))
	for from, to := range r.aliases {
		out = append(out, from+" -> "+to.String())
	}
	sort.Strings(out)
	return out
}
