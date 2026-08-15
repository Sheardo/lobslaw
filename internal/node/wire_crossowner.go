package node

import (
	"context"

	"github.com/jmylchreest/lobslaw/internal/audit"
	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/policy"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// CrossOwnerAction and CrossOwnerResource are what a cross-owner read
// is evaluated as. An operator grants it by writing a rule against
// them:
//
//	[[policy.rules]]
//	id       = "operators-read-any-memory"
//	subject  = "role:operator"
//	action   = "memory:read:any"
//	resource = "memory:*"
//	effect   = "allow"
//	priority = 50
//
// Nothing seeds that rule. A fresh deployment answers "may this
// operator read someone else's memory" with default-deny, which is
// the only safe answer to give on the operator's behalf.
const (
	CrossOwnerAction   = "memory:read:any"
	CrossOwnerResource = "memory:*"
)

// crossOwnerAuthorizer answers compute's cross-owner question from
// the policy engine, and records the answer when it is yes.
//
// The audit write lives here rather than at the reading call sites
// for the reason the decision gives for wanting it at all: the
// complaint that motivated the operator role was an audit trail the
// subject could write. One implementation, one place a widening can
// be granted, one place it is recorded — a future reader that forgets
// to log cannot exist, because logging is not the reader's job.
type crossOwnerAuthorizer struct {
	engine *policy.Engine
	audit  *audit.AuditLog
	log    logWarner
}

// logWarner is the slice of *slog.Logger this needs. Narrow so a test
// can pass a recorder without a handler.
type logWarner interface {
	Warn(msg string, args ...any)
}

// crossOwnerAuthz returns the authorizer, or nil when this node has no
// policy engine — a node that cannot evaluate rules cannot have been
// granted anything, and compute reads a nil authorizer as "no
// widening". Returning a typed nil here instead would defeat that:
// the interface would be non-nil and every read would call through to
// a nil engine.
func (n *Node) crossOwnerAuthz() compute.CrossOwnerAuthorizer {
	if n.policyEngine == nil {
		return nil
	}
	return &crossOwnerAuthorizer{
		engine: n.policyEngine,
		audit:  n.auditLog,
		log:    n.log,
	}
}

// AllowsAny evaluates the widening and fails closed on anything that
// is not an explicit allow.
//
// require_confirmation is treated as a refusal. Two of the three call
// sites — passive recall in the context engine, and the forget scope
// check — have no user in front of them to ask, so the honest
// options are "deny" or "silently allow", and an effect an operator
// chose specifically to slow something down must not become the one
// that speeds it up.
func (a *crossOwnerAuthorizer) AllowsAny(ctx context.Context, claims *types.Claims) bool {
	if claims == nil {
		return false
	}
	dec, err := a.engine.Evaluate(ctx, claims, CrossOwnerAction, CrossOwnerResource)
	if err != nil {
		a.log.Warn("cross-owner: policy evaluation failed; refusing to widen",
			"user_id", claims.UserID, "err", err)
		return false
	}
	if dec.Effect != types.EffectAllow {
		return false
	}
	a.record(ctx, claims, dec)
	return true
}

// record appends the widening to the audit chain. A failure to record
// does not fail the read: the entry has already been emitted to the
// logger, and turning an audit-sink outage into a read outage would
// hand anyone who can break a sink a way to deny service. The pairing
// is deliberate — the WARN is what an operator alerts on when the
// chain goes quiet.
func (a *crossOwnerAuthorizer) record(ctx context.Context, claims *types.Claims, dec policy.Decision) {
	if a.audit == nil {
		a.log.Warn("cross-owner: widened read with no audit sink configured",
			"user_id", claims.UserID, "scope", claims.Scope, "rule", dec.RuleID)
		return
	}
	err := a.audit.Append(ctx, types.AuditEntry{
		ActorScope: actorScope(claims),
		Action:     CrossOwnerAction,
		Target:     CrossOwnerResource,
		PolicyRule: dec.RuleID,
		Effect:     dec.Effect,
	})
	if err != nil {
		a.log.Warn("cross-owner: audit append failed",
			"user_id", claims.UserID, "rule", dec.RuleID, "err", err)
	}
}

// actorScope renders the caller in the "scope:user" shape the audit
// log already uses, without emitting a bare separator when one half
// is missing — an entry reading ":alice" invites the reader to guess
// which half was dropped.
func actorScope(c *types.Claims) string {
	switch {
	case c.Scope != "" && c.UserID != "":
		return c.Scope + ":" + c.UserID
	case c.UserID != "":
		return c.UserID
	default:
		return c.Scope
	}
}
