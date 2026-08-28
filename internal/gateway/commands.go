package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A runtime control surface, shared across channels.
//
// Built channel-agnostic on purpose. Hermes shares one command set
// between its CLI and every platform it speaks; the alternative — a
// Slack implementation, then a Telegram one, then a CLI one — is three
// places for "/new" to mean three slightly different things. Slack is
// what forced this to exist, because there a slash command is the
// native control surface rather than a nicety, but nothing below
// mentions Slack.

// CommandAuthorizer answers whether a caller may run a command.
//
// An interface here rather than a *policy.Engine so the gateway
// depends on the question and not the machinery, matching how
// CrossOwnerAuthorizer is defined over in compute. It also means a
// test can hand the dispatcher a two-line fake.
//
// Implementations MUST fail closed: an error, an unreachable rule
// store, or anything short of an explicit allow has to come back false.
type CommandAuthorizer interface {
	AllowsCommand(ctx context.Context, claims *types.Claims, name string) bool
}

// CommandAction is the policy action a command invocation is evaluated
// under. The resource is the command's own name, so a rule reads
// action = "command:exec", resource = "new".
const CommandAction = "command:exec"

// CommandRequest is one invocation, in channel-agnostic terms.
type CommandRequest struct {
	// Name is the command without its leading slash: "new", "help".
	Name string
	// Args is everything after the command name, untrimmed of internal
	// structure — a command that wants sub-arguments parses its own.
	Args string
	// Claims identifies the caller for the policy decision.
	Claims *types.Claims
	// Session addresses the conversation the command was run in, so a
	// command like /new knows what to reset.
	Session SessionRef
	// Shared marks a conversation others can read. A command that
	// would print something private needs to know.
	Shared bool
}

// CommandFunc runs a command and returns what to show the caller.
//
// The string is the reply. An error is reported to the caller too —
// a command that fails silently is worse than one that says so, since
// the user has no way to tell it apart from the bot ignoring them.
type CommandFunc func(ctx context.Context, req CommandRequest) (string, error)

// Command is one entry in the set.
type Command struct {
	Name string
	// Summary is one line, shown by /help.
	Summary string
	// Handler does the work.
	Handler CommandFunc
	// SharedSafe marks a command whose output is fine in a room others
	// can read. Anything false is refused outside a DM rather than
	// having its output quietly trimmed — a half-answer in a channel
	// is how somebody learns the wrong thing about what the bot knows.
	SharedSafe bool
}

// CommandSet is the registry and dispatcher.
type CommandSet struct {
	cmds  map[string]*Command
	authz CommandAuthorizer
	log   *slog.Logger
}

func NewCommandSet(authz CommandAuthorizer, log *slog.Logger) *CommandSet {
	if log == nil {
		log = slog.Default()
	}
	return &CommandSet{cmds: make(map[string]*Command), authz: authz, log: log}
}

// Register adds a command. A duplicate name replaces the earlier one,
// which is what lets a deployment override a built-in.
func (cs *CommandSet) Register(c *Command) {
	if cs == nil || c == nil || c.Name == "" {
		return
	}
	cs.cmds[strings.ToLower(c.Name)] = c
}

// Names returns the registered commands, sorted, for help and for the
// operator to mirror into a channel's own command registry.
func (cs *CommandSet) Names() []string {
	if cs == nil {
		return nil
	}
	out := make([]string, 0, len(cs.cmds))
	for n := range cs.cmds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Dispatch runs a command and returns the reply to show the caller.
//
// Never returns an error: every failure is something the caller needs
// to read, and a channel that had to decide how to render an error
// would render it differently per channel.
func (cs *CommandSet) Dispatch(ctx context.Context, req CommandRequest) string {
	if cs == nil {
		return "Commands are not available on this node."
	}
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(req.Name), "/"))
	if name == "" {
		return "No command given. Try `help`."
	}
	req.Name = name

	cmd, ok := cs.cmds[name]
	if !ok {
		return fmt.Sprintf("Unknown command %q. Try `help`.", name)
	}

	// Policy before anything else, and fail closed. A command is a
	// privileged operation in exactly the way a tool call is —
	// /new destroys a transcript — so it goes through the same
	// authorisation the tools do rather than trusting the channel's
	// coarse allowlist to have meant this too.
	if cs.authz == nil {
		cs.log.Warn("command refused: no authorizer wired", "command", name)
		return "Commands are not authorised on this node."
	}
	if !cs.authz.AllowsCommand(ctx, req.Claims, name) {
		cs.log.Info("command denied by policy", "command", name,
			"subject", subjectOf(req.Claims))
		return fmt.Sprintf("You're not allowed to run %q here.", name)
	}

	if req.Shared && !cmd.SharedSafe {
		return fmt.Sprintf("`%s` only works in a direct message — this conversation has an audience.", name)
	}

	out, err := cmd.Handler(ctx, req)
	if err != nil {
		cs.log.Warn("command failed", "command", name, "err", err)
		return fmt.Sprintf("`%s` failed: %v", name, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Sprintf("`%s` did nothing to report.", name)
	}
	return out
}

func subjectOf(c *types.Claims) string {
	if c == nil {
		return ""
	}
	return c.UserID
}

// --- built-in commands -------------------------------------------------

// ConversationResetter is what /new needs: a way to drop the stored
// transcript for one conversation. conversationLog satisfies it.
type ConversationResetter interface {
	Forget(ctx context.Context, ref SessionRef) error
	Load(ctx context.Context, ref SessionRef) Transcript
}

// RegisterBuiltinCommands installs the commands that need nothing
// beyond a conversation store.
//
// Deliberately a short list. The obvious extras — /usage, /stop,
// /skills, /model — each need a subsystem the gateway does not hold a
// handle to, and a command that half-works is worse than one that is
// not offered: the operator has no way to tell "not wired here" from
// "broken". They are additive once the handle exists.
func RegisterBuiltinCommands(cs *CommandSet, conv ConversationResetter) {
	if cs == nil {
		return
	}

	cs.Register(&Command{
		Name:       "help",
		Summary:    "list the commands you can run",
		SharedSafe: true,
		Handler: func(_ context.Context, _ CommandRequest) (string, error) {
			var b strings.Builder
			b.WriteString("Commands:\n")
			for _, n := range cs.Names() {
				c := cs.cmds[n]
				fmt.Fprintf(&b, "  %-10s %s\n", n, c.Summary)
			}
			return b.String(), nil
		},
	})

	cs.Register(&Command{
		Name:       "whoami",
		Summary:    "show who this node thinks you are",
		SharedSafe: false,
		Handler: func(_ context.Context, req CommandRequest) (string, error) {
			var b strings.Builder
			// Principal first: it is the answer to the question people
			// actually have, and the one that silently differs from the
			// raw channel id when an alias is missing.
			fmt.Fprintf(&b, "principal:    %s\n", orNone(req.Session.UserID))
			if req.Claims != nil {
				fmt.Fprintf(&b, "scope:        %s\n", orNone(req.Claims.Scope))
				fmt.Fprintf(&b, "roles:        %s\n", orNone(strings.Join(req.Claims.Roles, ", ")))
			}
			fmt.Fprintf(&b, "channel:      %s\n", req.Session.Channel)
			fmt.Fprintf(&b, "conversation: %s\n", req.Session.ChannelID)
			fmt.Fprintf(&b, "shared:       %t\n", req.Shared)
			return b.String(), nil
		},
	})

	if conv == nil {
		return
	}

	cs.Register(&Command{
		Name:       "status",
		Summary:    "show what this conversation is carrying",
		SharedSafe: true,
		Handler: func(ctx context.Context, req CommandRequest) (string, error) {
			t := conv.Load(ctx, req.Session)
			var b strings.Builder
			fmt.Fprintf(&b, "conversation: %s\n", req.Session.ChannelID)
			fmt.Fprintf(&b, "messages:     %d retained\n", len(t.Messages))
			if t.Summary != "" {
				fmt.Fprintf(&b, "summary:      %d chars of compacted history\n", len(t.Summary))
			} else {
				b.WriteString("summary:      none yet\n")
			}
			return b.String(), nil
		},
	})

	cs.Register(&Command{
		Name:       "new",
		Summary:    "forget this conversation and start fresh",
		SharedSafe: true,
		Handler: func(ctx context.Context, req CommandRequest) (string, error) {
			if err := conv.Forget(ctx, req.Session); err != nil {
				return "", err
			}
			return "Started a fresh conversation. I've forgotten what we were talking about here.", nil
		},
	})
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
