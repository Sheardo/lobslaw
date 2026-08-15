package types

import "time"

// NodeID uniquely identifies a cluster node (UUID, assigned at
// first start, persisted).
type NodeID string

// NodeFunction is one of the roles a lobslaw binary can enable. A
// single binary can enable any subset; a single-node deployment
// enables all of them.
type NodeFunction string

// The available node functions. These are the wire values used in
// [cluster].functions and advertised in NodeInfo.
const (
	// FunctionMemory serves the vector + episodic memory store and
	// participates in the raft group backing it.
	FunctionMemory NodeFunction = "memory"
	// FunctionPolicy is DEPRECATED and normalised away to
	// FunctionMemory. It never selected anything of its own: both the
	// policy and memory gRPC services are gated on "this node hosts
	// raft", which memory already implies, and rule enforcement is
	// wired from the store rather than from this bit. An operator
	// declaring it got a memory node and no warning.
	//
	// Accepted so existing configs keep booting. See R25.
	FunctionPolicy NodeFunction = "policy"
	// FunctionCompute runs the agent loop, tool registry and builtins.
	FunctionCompute NodeFunction = "compute"
	// FunctionGateway terminates inbound channels (Telegram, HTTP).
	FunctionGateway NodeFunction = "gateway"
	// FunctionStorage serves the object store and its sandboxed
	// nested filesystem mounts.
	FunctionStorage NodeFunction = "storage"
)

// IsValid reports whether f is a known function, so an unrecognised
// entry in [cluster].functions fails at boot rather than starting a
// node that silently serves nothing.
func (f NodeFunction) IsValid() bool {
	switch f {
	case FunctionMemory, FunctionPolicy, FunctionCompute, FunctionGateway, FunctionStorage:
		return true
	}
	return false
}

// NodeInfo is advertised on registration and heartbeat. Peer
// identity for security comes from the mTLS cert SAN — ID is
// advisory.
type NodeInfo struct {
	ID           NodeID         `json:"id"`
	Functions    []NodeFunction `json:"functions"`
	Address      string         `json:"address"`
	Capabilities []string       `json:"capabilities,omitempty"`
	RaftMember   bool           `json:"raft_member"`
}

// HealthStatus is one node's self-reported health, as returned by
// the health endpoint and gossiped on heartbeat. Status is the
// rolled-up view; Components carries the per-subsystem detail behind
// it.
type HealthStatus struct {
	NodeID     NodeID            `json:"node_id"`
	Status     HealthLevel       `json:"status"`
	LastSeen   time.Time         `json:"last_seen"`
	Components []ComponentHealth `json:"components,omitempty"`
}

// HealthLevel grades a node or one of its components.
type HealthLevel string

// The health levels, best to worst.
const (
	// HealthHealthy means every component is serving normally.
	HealthHealthy HealthLevel = "healthy"
	// HealthDegraded means the node is still serving but at least one
	// component is impaired — it should not be taken out of rotation.
	HealthDegraded HealthLevel = "degraded"
	// HealthUnhealthy means the node cannot serve its functions.
	HealthUnhealthy HealthLevel = "unhealthy"
)

// ComponentHealth is one subsystem's contribution to a node's
// HealthStatus. Error carries the reason whenever Status is not
// healthy.
type ComponentHealth struct {
	Name   string      `json:"name"`
	Status HealthLevel `json:"status"`
	Error  string      `json:"error,omitempty"`
}

// NormalizeFunctions resolves deprecated aliases and removes
// duplicates, preserving declaration order.
//
// It reports the aliases it rewrote so the caller can warn once,
// rather than every consumer of the list rediscovering that policy
// and memory are the same thing.
func NormalizeFunctions(fns []NodeFunction) (out []NodeFunction, rewrote []NodeFunction) {
	seen := make(map[NodeFunction]bool, len(fns))
	for _, f := range fns {
		canonical := f
		if f == FunctionPolicy {
			canonical = FunctionMemory
			rewrote = append(rewrote, f)
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out, rewrote
}
