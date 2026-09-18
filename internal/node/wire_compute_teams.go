package node

import (
	"fmt"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/tools"
)

func (n *Node) wireComputeTeamsStage() error {
	if n.botSvc != nil && n.groupSvc == nil {
		n.groupSvc = memory.NewGroupService(n.raft, n.store)
	}
	if n.inboxSvc == nil && n.raft != nil && n.store != nil {
		n.inboxSvc = memory.NewInboxService(n.raft, n.store, 0)
	}
	if n.inboxWake == nil {
		n.inboxWake = make(chan struct{}, 1)
	}
	return n.wireTeamTools()
}

func (n *Node) wireTeamTools() error {
	if n.builtinsRegistry == nil || n.toolRegistry == nil || n.botSvc == nil {
		return nil
	}
	if err := tools.RegisterBotBuiltins(n.builtinsRegistry, tools.BotConfig{
		Registry: n.botSvc,
		Resolver: botResolverOrNil(n.botSvc),
		Runner:   n.agent,
		Inbox:    n.inboxSvc,
	}); err != nil {
		return err
	}
	if err := tools.RegisterInboxBuiltins(n.builtinsRegistry, tools.InboxConfig{
		Service: n.inboxSvc,
		Bots:    botResolverOrNil(n.botSvc),
	}); err != nil {
		return err
	}
	for _, td := range append(tools.BotToolDefs(), tools.InboxToolDefs()...) {
		if err := n.toolRegistry.Register(td); err != nil {
			return fmt.Errorf("register team tool %q: %w", td.Name, err)
		}
	}
	return nil
}
