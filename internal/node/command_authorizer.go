package node

import (
	"context"

	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// commandAuthorizer answers gateway.CommandAuthorizer from the policy
// engine.
//
// Slash commands go through the same authorisation as tool calls
// because they are the same kind of thing: /new destroys a transcript,
// and a future /model crossing a trust tier is a policy decision rather
// than a UI preference. Routing them anywhere else would give the
// operator two places to express one intent — and the channel's
// allowlist, which is the only other gate, says who may TALK to the
// bot, not what they may make it do.
type commandAuthorizer struct {
	n *Node
}

// AllowsCommand evaluates action "command:exec" against the command
// name as the resource, so a rule reads:
//
//	subject = "role:operator", action = "command:exec", resource = "new"
//
// Fails CLOSED at every step. No engine, an evaluation error, or
// anything short of an explicit allow comes back false — an
// authorisation question this node cannot answer is not one it should
// answer optimistically.
func (a commandAuthorizer) AllowsCommand(ctx context.Context, claims *types.Claims, name string) bool {
	if a.n == nil || a.n.policyEngine == nil {
		return false
	}
	dec, err := a.n.policyEngine.Evaluate(ctx, claims, gateway.CommandAction, name)
	if err != nil {
		a.n.log.Warn("command authorizer: policy evaluate failed; refusing",
			"command", name, "err", err)
		return false
	}
	// Only a plain allow. require_confirmation is deliberately treated
	// as a refusal rather than plumbed into the prompt flow: a command
	// is a single synchronous exchange with no continuation to resume,
	// so there is nothing for an approval to come back to.
	return dec.Effect == types.EffectAllow
}

// commandAuthorizerOrNil returns the authorizer, or nil when this node
// has no policy engine. Nil makes the CommandSet refuse everything,
// which is the correct reading of a node that cannot authorise.
func (n *Node) commandAuthorizerOrNil() gateway.CommandAuthorizer {
	if n.policyEngine == nil {
		return nil
	}
	return commandAuthorizer{n: n}
}
