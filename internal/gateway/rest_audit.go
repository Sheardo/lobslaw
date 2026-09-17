package gateway

import (
	"context"
	"net/http"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// RegistryAuditor records who changed a bot or a team.
//
// A gateway-local interface over audit.AuditLog, for the same reason
// as BotAPI: the gateway should not import the audit package to write
// one line.
type RegistryAuditor interface {
	Append(ctx context.Context, entry types.AuditEntry) error
}

// auditRegistry records a change to the bot or team registry.
//
// These edits went unrecorded. While the console was a single shared
// secret that was defensible — every session was the same anonymous
// subject, so "who rewrote this bot's brief" had no answer to record.
// Per-user logins gave it one, and leaving it unwritten would mean
// the only lasting trace of somebody changing what a bot does is the
// changed bot.
//
// Best-effort: a failed audit write is logged, never returned. The
// edit has already been applied by the time we get here, so failing
// the response would report an error for work that succeeded and
// invite the operator to do it twice.
func (s *Server) auditRegistry(r *http.Request, action, target, detail string) {
	if s.cfg.Audit == nil {
		return
	}
	actor := s.principalOf(r)
	if actor == "" {
		actor = "console"
	}
	entry := types.AuditEntry{
		ActorScope: actor,
		Action:     action,
		Target:     target,
		Effect:     types.EffectAllow,
		// The name, not the whole record: an audit line is for
		// answering "who changed this and roughly what", and a bot's
		// instructions can be kilobytes of prose.
		Argv: []string{detail},
		// The console authorises through its session rather than a
		// policy rule, and naming a rule that did not fire would be
		// worse than naming none.
		PolicyRule: "console",
	}
	if err := s.cfg.Audit.Append(r.Context(), entry); err != nil {
		s.log.Warn("audit: registry change not recorded",
			"action", action, "target", target, "err", err)
	}
}
