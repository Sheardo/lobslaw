package memory

import (
	"github.com/jmylchreest/lobslaw/internal/identity"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Audience is who a read is on behalf of. Every search takes one.
//
// It exists as a type rather than a string because the string version
// already failed. Search used to take a `scopeFilter string` where ""
// meant "everything", and both production call sites passed "" — the
// agent's memory_search tool and, worse, the ContextEngine, which
// injects recalled memories into the system prompt on every turn with
// no tool call in front of it. On a shared node that put one user's
// memories into another's prompt before they had said anything.
//
// The lesson was not that two callers were careless. It was that the
// dangerous value was the easy one to write: "" is what you get from a
// zero value, an unset variable, or not thinking about it. So here the
// zero Audience matches nothing, and seeing everything has to be
// spelled Everyone() — three call sites want it and each is a caller
// that already holds the whole database.
type Audience struct {
	// set distinguishes the zero value from a deliberate anonymous
	// audience. Without it, Audience{} and For("") are the same, and
	// the accident is indistinguishable from the intent.
	set bool
	// everyone bypasses the filter entirely.
	everyone bool
	// principal is the canonical identity the read is for.
	principal identity.Principal
}

// For returns the audience for a principal. A zero principal — an
// anonymous turn — still produces a set Audience: anonymous means
// "owns nothing", not "sees nothing", so such a caller still reads
// shared and legacy records.
func For(p identity.Principal) Audience {
	return Audience{set: true, principal: p}
}

// Everyone is the unrestricted read, spelled out so it can be grepped
// and reviewed. Legitimate for Dream consolidation, the operator's
// cmd/inspect, and compaction — callers that hold the whole store
// already and gain nothing from being refused a view of it.
func Everyone() Audience {
	return Audience{set: true, everyone: true}
}

// IsZero reports an Audience nobody set. Callers that can refuse
// should; the search paths treat it as matching nothing.
func (a Audience) IsZero() bool { return !a.set }

// allows decides one record.
//
// Three ways in, in order of how often they matter:
//
//   - Shared. Operator-seeded knowledge and anything about the
//     deployment rather than about a person.
//   - Owned. The principal matches.
//
// An UNOWNED record is readable by nobody but Everyone(). There used to
// be a carve-out making it readable by all, on the grounds that an
// upgrade must not hide an existing node's whole memory — but lobslaw
// has never been deployed, so there are no records predating ownership
// and that carve-out was a standing fail-open guarding the empty set.
// Nothing writes an empty owner either: every Claims construction in
// the tree yields one ("anon" for unauthenticated REST,
// "webhook:<name>", "scheduler", the Telegram identity), so an unowned
// record now means a bug upstream, and being invisible is how it
// surfaces rather than how it hides.
func (a Audience) allows(owner string, vis lobslawv1.Visibility) bool {
	if !a.set {
		return false
	}
	if a.everyone {
		return true
	}
	if vis == lobslawv1.Visibility_VISIBILITY_SHARED {
		return true
	}
	return !a.principal.IsZero() && owner == a.principal.String()
}

// AllowsVector reports whether a vector record is readable. Exported
// for callers that filter a result set they already hold.
func (a Audience) AllowsVector(rec *lobslawv1.VectorRecord) bool {
	if rec == nil {
		return false
	}
	return a.allows(rec.Owner, rec.Visibility)
}

// AllowsEpisodic reports whether an episodic record is readable.
func (a Audience) AllowsEpisodic(rec *lobslawv1.EpisodicRecord) bool {
	if rec == nil {
		return false
	}
	return a.allows(rec.Owner, rec.Visibility)
}
