package node

import (
	"fmt"

	"github.com/jmylchreest/lobslaw/internal/memory"
)

// wireSelfTaught constructs the store the agent writes its own
// instructions into — or does not.
//
// With mode = "off" this returns nil having built nothing, so
// n.selfTaught stays nil and every dependent is ABSENT rather than
// guarded. That distinction is the whole point: "the capability is not
// present" is a different and stronger claim than "the call sites
// check a flag", and the second is not what an operator disabling
// self-learning is asking for.
func (n *Node) wireSelfTaught() error {
	mode := memory.ParseSelfLearningMode(n.cfg.SelfLearningMode)
	if mode == memory.SelfLearningOff {
		// Said out loud at boot. Silence here is indistinguishable
		// from a wiring bug, and an operator who meant to enable it
		// should find out now rather than by wondering why nothing
		// was ever learned.
		n.log.Info("self-learning: disabled; the store is not wired and no artefact can be written")
		return nil
	}

	store, err := memory.NewSelfTaughtStore(n.raft, n.store, mode)
	if err != nil {
		return fmt.Errorf("self-taught store: %w", err)
	}
	n.selfTaught = store
	n.log.Info("self-learning: enabled", "mode", string(mode),
		"artefacts_active_immediately", mode == memory.SelfLearningAuto)
	return nil
}
