package compute

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// SessionBrowser is the read side of the transcript store, as the
// agent's session tools need it. Narrow interface for the usual
// import-cycle reason; the node wires a memory.SessionService adapter.
type SessionBrowser interface {
	// Search finds conversations containing text.
	Search(ctx context.Context, q SessionBrowseQuery) ([]SessionBrowseHit, error)
	// Recent lists conversations newest-first.
	Recent(ctx context.Context, limit int) ([]SessionBrowseInfo, error)
	// Read returns a window of one conversation's transcript.
	Read(ctx context.Context, key SessionKey, fromSeq uint64, limit int) ([]Message, error)
}

// SessionBrowseQuery mirrors memory.SessionSearchQuery.
type SessionBrowseQuery struct {
	Text               string
	Channel            string
	UserID             string
	Limit              int
	SnippetsPerSession int
}

// SessionBrowseInfo is a conversation's index entry.
type SessionBrowseInfo struct {
	Channel   string
	ChannelID string
	Title     string
	UserID    string
	Messages  uint64
	UpdatedAt string
	Summary   string
}

// SessionBrowseHit is one search result.
type SessionBrowseHit struct {
	Info     SessionBrowseInfo
	Matches  int
	Snippets []SessionBrowseSnippet
}

// SessionBrowseSnippet locates a match within a transcript.
type SessionBrowseSnippet struct {
	Seq  uint64
	Role string
	Text string
}

// SessionToolConfig bounds what the session tools may return. Every
// result goes into the agent's context window, so an unbounded
// session_read would undo the context budget in a single tool call.
type SessionToolConfig struct {
	Browser SessionBrowser
	// MaxSearchResults caps conversations per search. 0 → 5.
	MaxSearchResults int
	// MaxSnippets caps snippets per conversation. 0 → 3.
	MaxSnippets int
	// MaxReadMessages caps messages per session_read. 0 → 40.
	MaxReadMessages int
}

// Session tool defaults.
const (
	DefaultMaxSearchResults = 5
	DefaultMaxSnippets      = 3
	DefaultMaxReadMessages  = 40
)

// RegisterSessionBuiltins wires session_search / session_list /
// session_read.
func RegisterSessionBuiltins(b *Builtins, cfg SessionToolConfig) error {
	if cfg.Browser == nil {
		return errors.New("session builtins: Browser required")
	}
	if cfg.MaxSearchResults <= 0 {
		cfg.MaxSearchResults = DefaultMaxSearchResults
	}
	if cfg.MaxSnippets <= 0 {
		cfg.MaxSnippets = DefaultMaxSnippets
	}
	if cfg.MaxReadMessages <= 0 {
		cfg.MaxReadMessages = DefaultMaxReadMessages
	}
	if err := b.Register("session_search", newSessionSearchHandler(cfg)); err != nil {
		return err
	}
	if err := b.Register("session_list", newSessionListHandler(cfg)); err != nil {
		return err
	}
	return b.Register("session_read", newSessionReadHandler(cfg))
}

func newSessionSearchHandler(cfg SessionToolConfig) BuiltinFunc {
	return func(ctx context.Context, args map[string]string) ([]byte, int, error) {
		query := strings.TrimSpace(args["query"])
		if query == "" {
			return nil, 2, errors.New("query is required")
		}
		limit := clampArg(args["limit"], cfg.MaxSearchResults, cfg.MaxSearchResults)
		hits, err := cfg.Browser.Search(ctx, SessionBrowseQuery{
			Text:               query,
			Channel:            strings.TrimSpace(args["channel"]),
			Limit:              limit,
			SnippetsPerSession: cfg.MaxSnippets,
		})
		if err != nil {
			return nil, 1, err
		}
		if len(hits) == 0 {
			return []byte(fmt.Sprintf("No past conversation contains %q.", query)), 0, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d conversation(s) mention %q:\n", len(hits), query)
		for _, h := range hits {
			fmt.Fprintf(&b, "\n%s (%d match(es), last active %s)\n",
				describeSession(h.Info), h.Matches, h.Info.UpdatedAt)
			for _, s := range h.Snippets {
				fmt.Fprintf(&b, "  [#%d %s] %s\n", s.Seq, s.Role, collapseWhitespace(s.Text))
			}
		}
		b.WriteString("\nUse session_read with the channel and channel_id to see more.")
		return []byte(b.String()), 0, nil
	}
}

func newSessionListHandler(cfg SessionToolConfig) BuiltinFunc {
	return func(ctx context.Context, args map[string]string) ([]byte, int, error) {
		limit := clampArg(args["limit"], 10, 50)
		infos, err := cfg.Browser.Recent(ctx, limit)
		if err != nil {
			return nil, 1, err
		}
		if len(infos) == 0 {
			return []byte("No stored conversations yet."), 0, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d conversation(s), most recent first:\n", len(infos))
		for _, i := range infos {
			fmt.Fprintf(&b, "  %s — %d messages, last active %s\n",
				describeSession(i), i.Messages, i.UpdatedAt)
		}
		return []byte(b.String()), 0, nil
	}
}

func newSessionReadHandler(cfg SessionToolConfig) BuiltinFunc {
	return func(ctx context.Context, args map[string]string) ([]byte, int, error) {
		channel := strings.TrimSpace(args["channel"])
		channelID := strings.TrimSpace(args["channel_id"])
		if channel == "" || channelID == "" {
			return nil, 2, errors.New("channel and channel_id are required (both come from session_search or session_list)")
		}
		var fromSeq uint64
		if v := strings.TrimSpace(args["from_seq"]); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, 2, fmt.Errorf("from_seq must be a number: %w", err)
			}
			fromSeq = n
		}
		limit := clampArg(args["limit"], cfg.MaxReadMessages, cfg.MaxReadMessages)
		msgs, err := cfg.Browser.Read(ctx, SessionKey{Channel: channel, ChannelID: channelID}, fromSeq, limit)
		if err != nil {
			return nil, 1, err
		}
		if len(msgs) == 0 {
			return []byte("That conversation has no messages in that range."), 0, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s:%s, %d message(s) from #%d:\n", channel, channelID, len(msgs), fromSeq)
		for _, m := range msgs {
			b.WriteString(renderForSummary(m, DefaultCompactToolResultBytes))
		}
		return []byte(b.String()), 0, nil
	}
}

// describeSession prefers the generated title, falling back to the
// address so a result is always actionable for session_read.
func describeSession(i SessionBrowseInfo) string {
	label := i.Title
	if strings.TrimSpace(label) == "" {
		label = "(untitled)"
	}
	return fmt.Sprintf("%q [%s:%s]", label, i.Channel, i.ChannelID)
}

// clampArg parses an optional numeric arg, falling back to def and
// never exceeding max — the model doesn't get to widen its own limits.
func clampArg(raw string, def, max int) int {
	v := def
	if s := strings.TrimSpace(raw); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			v = n
		}
	}
	if v > max {
		v = max
	}
	return v
}

// collapseWhitespace flattens a snippet to one line so a multi-line
// match doesn't wreck the result list's readability.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// SessionToolDefs describes the session tools to the LLM.
func SessionToolDefs() []*types.ToolDef {
	return []*types.ToolDef{
		{
			Name:        "session_search",
			Path:        BuiltinScheme + "session_search",
			Description: "Search the exact text of past conversations. Use when the user refers to something specific that was said before — a command they ran, an error message, a name, a decision — and you need the actual wording rather than a general recollection. For 'what do you know about X' use memory_search instead; this finds literal text in a specific thread. Returns matching conversations with snippets; follow up with session_read to see more.",
			ParametersSchema: []byte(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Literal text to find in past messages."},
					"channel": {"type": "string", "description": "Optional channel filter, e.g. \"telegram\" or \"rest\"."},
					"limit": {"type": "integer", "description": "Max conversations to return."}
				},
				"required": ["query"],
				"additionalProperties": false
			}`),
			RiskTier: types.RiskReversible,
		},
		{
			Name:        "session_list",
			Path:        BuiltinScheme + "session_list",
			Description: "List stored conversations, most recently active first, with their titles and message counts. Use when the user asks what you've been talking about, or to find a thread by topic before reading it with session_read.",
			ParametersSchema: []byte(`{
				"type": "object",
				"properties": {
					"limit": {"type": "integer", "description": "Max conversations to list. Default 10."}
				},
				"additionalProperties": false
			}`),
			RiskTier: types.RiskReversible,
		},
		{
			Name:        "session_read",
			Path:        BuiltinScheme + "session_read",
			Description: "Read a window of a stored conversation's transcript. Takes the channel and channel_id from session_search or session_list. Use from_seq to page through a long thread — each result reports the sequence numbers it covers. Prefer session_search first; reading a whole conversation is expensive in context.",
			ParametersSchema: []byte(`{
				"type": "object",
				"properties": {
					"channel": {"type": "string", "description": "Channel kind, from session_search or session_list."},
					"channel_id": {"type": "string", "description": "Conversation id, from session_search or session_list."},
					"from_seq": {"type": "integer", "description": "Start at this sequence number. Omit to start at the beginning."},
					"limit": {"type": "integer", "description": "Max messages to return."}
				},
				"required": ["channel", "channel_id"],
				"additionalProperties": false
			}`),
			RiskTier: types.RiskReversible,
		},
	}
}
