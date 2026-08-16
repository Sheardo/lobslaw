package node

import (
	"log/slog"
	"testing"
)

// The acceptance criterion says this specifically: asserted by wiring,
// not by mocking a flag. "The capability is not present" is a
// different and stronger claim than "the call sites check a setting",
// and the second is not what an operator disabling self-learning is
// asking for.

func TestOffLeavesNoStoreWired(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]string{
		"explicitly off": "off",
		"unset":          "",
		"a typo":         "enabld",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n := &Node{
				log: slog.New(slog.DiscardHandler),
				cfg: Config{SelfLearningMode: mode},
			}
			if err := n.wireSelfTaught(); err != nil {
				t.Fatalf("wiring errored: %v", err)
			}
			if n.selfTaught != nil {
				t.Errorf("mode %q produced a store; the capability should be absent", mode)
			}
		})
	}
}

// And the converse: an operator who asked for it gets it. A test that
// only checked the off case would pass on a wiring function that never
// built anything.
func TestEnabledModesWireAStore(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"propose", "auto"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			n, _ := pinnedNode(t) // reuses the raft-backed fixture
			n.cfg.SelfLearningMode = mode
			if err := n.wireSelfTaught(); err != nil {
				t.Fatalf("wiring errored: %v", err)
			}
			if n.selfTaught == nil {
				t.Fatalf("mode %q wired no store", mode)
			}
			if got := string(n.selfTaught.Mode()); got != mode {
				t.Errorf("store mode = %q, want %q", got, mode)
			}
		})
	}
}
