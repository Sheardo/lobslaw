package node

import (
	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Staging agent-initiated memory writes.
//
// Two halves, and both are needed: the executor has to KNOW that
// memory_write is gated, and the policy engine has to have something
// to say when asked. Wiring one without the other fails in opposite
// directions — a gate with no rule denies every write by default-deny,
// and a rule with no gate is never consulted.

// wireWriteApproval installs the gate and its default rule.
//
// Off means ABSENT: nothing is registered, so a deployment that never
// opted in carries no extra check rather than a check that always
// passes.
func (n *Node) wireWriteApproval() error {
	if !n.cfg.MemoryWriteApproval {
		return nil
	}
	if n.executor == nil || n.policyEngine == nil {
		// Said out loud. An operator who set the flag and got silence
		// would reasonably conclude their memories were being staged
		// when in fact every one of them is landing.
		n.log.Warn("memory: write_approval is set but there is no executor or policy engine; "+
			"agent-initiated writes are NOT being staged",
			"has_executor", n.executor != nil, "has_policy", n.policyEngine != nil)
		return nil
	}

	// The rule first. Registering the gate before the engine has
	// anything to say would make every write hit default-deny in the
	// window between the two.
	n.policyEngine.SetDefaults([]types.PolicyRule{compute.WriteApprovalDefault()})
	n.executor.RequireApproval("memory_write", "episodic", compute.MemoryWriteSummary)

	n.log.Info("memory: agent-initiated writes are staged for approval",
		"action", compute.ApprovalAction,
		"override", "write a policy rule of higher priority, or approve with scope=always")
	return nil
}
