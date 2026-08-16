package node

import (
	"context"

	"github.com/jmylchreest/lobslaw/internal/memory"
)

// grantsAdapter satisfies compute.DurableGrants over the replicated
// store.
//
// Narrow on purpose: compute can record a grant and ask whether one
// exists, and nothing else. Listing, revoking and sweeping stay on the
// store, reachable from the CLI and the wiring — the turn path has no
// business revoking a grant, and an interface that let it would make
// that an accident away.
type grantsAdapter struct{ inner *memory.SessionGrantStore }

func (a grantsAdapter) Grant(ctx context.Context, sessionID, action, resource, grantedBy string) error {
	_, err := a.inner.Grant(ctx, memory.GrantRequest{
		SessionID: sessionID,
		Action:    action,
		Resource:  resource,
		GrantedBy: grantedBy,
	})
	return err
}

func (a grantsAdapter) Granted(sessionID, action, resource string) bool {
	return a.inner.Granted(sessionID, action, resource)
}
