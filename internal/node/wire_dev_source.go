package node

import (
	"os"

	"github.com/jmylchreest/lobslaw/internal/skills"
)

// The dev source, checked at boot and refused if ungated.
//
// Validated in the compute wiring stage rather than lazily at scan
// time, because the answer to "this is configured but not gated" is to
// refuse to start — and a node that has already begun serving turns
// cannot un-answer the ones it has taken.

// checkDevSource refuses to proceed when a dev source is configured
// without LOBSLAW_DEV.
func (n *Node) checkDevSource() error {
	return skills.CheckDevSource(n.cfg.Skills.DevSource, os.Getenv(skills.DevMarkerEnv))
}

// loadDevSource scans the dev source into the registry.
//
// Called on every reconcile alongside the store loader, so an edit in
// the dev directory is picked up without a restart. That is the point
// of the source: the alternative is an operator restarting the node to
// try a one-line change to a skill.
func (n *Node) loadDevSource() {
	dir := n.cfg.Skills.DevSource
	if dir == "" || n.skillRegistry == nil {
		return
	}
	for _, err := range n.skillRegistry.ScanDev(dir) {
		n.log.Warn("skills: dev source scan error", "err", err)
	}
}
