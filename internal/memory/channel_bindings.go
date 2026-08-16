package memory

import (
	"context"
	"strings"
)

// PrincipalFor resolves a channel address to the canonical user id
// bound to it, satisfying identity.ChannelBindings.
//
// Distinct from FindByChannelAddress in one way that matters: an
// address nobody has bound returns ("", nil) rather than an error. The
// caller has to tell "nobody claims this address" — ordinary, and the
// case for every new person who messages the bot — apart from "the
// lookup failed", which is an outage and must not silently reassign
// somebody's identity.
func (s *UserPrefsService) PrincipalFor(ctx context.Context, channel, address string) (string, error) {
	channel, address = strings.TrimSpace(channel), strings.TrimSpace(address)
	if channel == "" || address == "" {
		return "", nil
	}
	all, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range all {
		for _, c := range p.Channels {
			// Case-insensitive for the same reason the alias map is:
			// channels disagree about whether they preserve the case
			// of a handle, and "@Alice" being a different person from
			// "@alice" is never what an operator meant. Numeric ids
			// are unaffected either way.
			if strings.EqualFold(c.Type, channel) && strings.EqualFold(c.Address, address) {
				return p.UserId, nil
			}
		}
	}
	return "", nil
}
