package node

import (
	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The gates that ask before something happens: staging agent-initiated
// memory writes, and per-command approval for shell_command.
//
// Two halves each, and both are needed: the executor has to KNOW the
// tool is gated, and the policy engine has to have something to say
// when asked. Wiring one without the other fails in opposite
// directions — a gate with no rule denies everything by default-deny,
// and a rule with no gate is never consulted.
//
// They are wired TOGETHER because Engine.SetDefaults replaces rather
// than appends. Two stages each calling it with their own one-element
// slice meant whichever ran second silently disabled the first, and
// the symptom would have been a gate that never asked — the failure
// mode that looks exactly like working correctly.

// wireApprovalGates installs every approval gate and the single set of
// default rules behind them.
func (n *Node) wireApprovalGates() error {
	var defaults []types.PolicyRule

	if n.cfg.MemoryWriteApproval {
		if n.executor == nil || n.policyEngine == nil {
			// Said out loud. An operator who set the flag and got
			// silence would reasonably conclude their memories were
			// being staged when in fact every one of them is landing.
			n.log.Warn("memory: write_approval is set but there is no executor or policy engine; "+
				"agent-initiated writes are NOT being staged",
				"has_executor", n.executor != nil, "has_policy", n.policyEngine != nil)
		} else {
			defaults = append(defaults, compute.WriteApprovalDefault())
			n.executor.RequireApproval("memory_write", "episodic", compute.MemoryWriteSummary)
			n.log.Info("memory: agent-initiated writes are staged for approval",
				"action", compute.ApprovalAction,
				"override", "write a policy rule of higher priority, or approve with scope=always")
		}
	}

	// Read off the registry rather than a flag: the gate must exist
	// exactly when the tool does, and a deployment that never
	// registered shell_command should not carry a rule about it.
	if n.shellIsRegistered() {
		if n.executor == nil || n.policyEngine == nil {
			n.log.Warn("compute: shell_command is registered but there is no executor or policy engine; "+
				"commands are NOT being approved",
				"has_executor", n.executor != nil, "has_policy", n.policyEngine != nil)
		} else {
			defaults = append(defaults, compute.ShellApprovalDefault())
			n.executor.RequireCommandApproval("shell_command",
				compute.ShellGrantResource, compute.ShellCommandSummary)
			n.log.Info("compute: shell commands are approved per command",
				"action", compute.ShellAction,
				"override", `write a policy rule, e.g. action="shell:run" resource="git *"`)
		}
	}

	// Once, and unconditionally — including with an empty slice, so a
	// node that turned a gate off clears the rule rather than leaving
	// the previous boot's default in place.
	//
	// The rules go in before the gates above start being consulted;
	// registering a gate while the engine had nothing to say would
	// make every call hit default-deny in the window between.
	if n.policyEngine != nil {
		n.policyEngine.SetDefaults(defaults)
	}
	return nil
}

func (n *Node) shellIsRegistered() bool {
	if n.toolRegistry == nil {
		return false
	}
	_, ok := n.toolRegistry.Get("shell_command")
	return ok
}
