package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// sessionUsage carries the stopped-node constraint for the group, the
// same way memoryUsage does — both groups open state.db directly.
const sessionUsage = `lobslaw session — read stored conversation transcripts offline

The node must be STOPPED. These subcommands open state.db directly and
bbolt takes an exclusive lock on the file, so a running node makes every
one of them fail.

subcommands:
  list             one line per conversation, plus its running summary
  show <id>        the full transcript of one conversation
  search <text>    substring search across every transcript

Read-only: forgetting a conversation is a replicated operation and goes
through the running node, not through here.`

// dispatchSession handles `lobslaw session <subcmd>`.
func dispatchSession(args []string) bool {
	idx := findSubcmd(args, "session")
	if idx < 0 {
		return false
	}
	sub := args[idx+1:]
	if len(sub) == 0 {
		fmt.Fprintln(os.Stderr, sessionUsage)
		os.Exit(2)
	}
	switch sub[0] {
	case "list":
		runOffline("session list", sessionList, sub[1:])
	case "show":
		runOffline("session show", sessionShow, sub[1:])
	case "search":
		runOffline("session search", sessionSearch, sub[1:])
	default:
		fmt.Fprintf(os.Stderr, "lobslaw session: unknown subcommand %q\n\n", sub[0])
		fmt.Fprintln(os.Stderr, sessionUsage)
		os.Exit(2)
	}
	return true
}

func sessionList(args []string) error {
	fs := flag.NewFlagSet("session list", flag.ExitOnError)
	var opts offlineStore
	opts.bind(fs)
	channel := fs.String("channel", "", "only conversations on this channel kind (telegram, rest, ...)")
	user := fs.String("user", "", "only conversations opened by this canonical user id")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, _, err := opts.open()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	records, err := listSessions(store, *channel, *user)
	if err != nil {
		return err
	}

	if *asJSON {
		out := make([]map[string]any, 0, len(records))
		for _, r := range records {
			out = append(out, sessionFields(r))
		}
		return emitJSON(map[string]any{"sessions": out, "total": len(records)})
	}

	for _, r := range records {
		fmt.Printf("\n  %s  channel=%s user=%s  retained=%d (seq %d..%d)  updated=%s\n",
			r.Id, orNone(r.Channel), orNone(r.UserId), retained(r), r.FirstSeq, lastSeq(r), orNone(tsString(r.UpdatedAt)))
		if r.Title != "" {
			fmt.Printf("    title: %s\n", r.Title)
		}
		if r.Summary != "" {
			fmt.Printf("    summary (through seq %d): %s\n", r.SummaryThroughSeq, collapse(r.Summary))
		}
	}
	fmt.Printf("\nTotal sessions: %d\n", len(records))
	if len(records) > 0 {
		fmt.Println("run `lobslaw session show <id>` for a full transcript")
	}
	return nil
}

func sessionShow(args []string) error {
	fs := flag.NewFlagSet("session show", flag.ExitOnError)
	var opts offlineStore
	opts.bind(fs)
	trunc := fs.Int("truncate", 0, "cap each message at N characters (0 = full text)")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("exactly one session id required (as printed by `lobslaw session list`)")
	}
	id := fs.Arg(0)

	store, _, err := opts.open()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	raw, err := store.Get(memory.BucketSessions, id)
	if err != nil {
		if memory.IsNotFound(err) {
			return fmt.Errorf("no session with id %q — `lobslaw session list` prints the valid ids", id)
		}
		return err
	}
	var rec lobslawv1.SessionRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("unmarshal session %q: %w", id, err)
	}

	msgs, err := loadMessages(store, id)
	if err != nil {
		return err
	}

	if *asJSON {
		out := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, messageFields(m, *trunc))
		}
		return emitJSON(map[string]any{"session": sessionFields(&rec), "messages": out})
	}

	fmt.Printf("%s  channel=%s user=%s  retained=%d (seq %d..%d)\n",
		rec.Id, orNone(rec.Channel), orNone(rec.UserId), retained(&rec), rec.FirstSeq, lastSeq(&rec))
	fmt.Printf("  title:   %s\n", orNone(rec.Title))
	fmt.Printf("  created: %s\n", orNone(tsString(rec.CreatedAt)))
	fmt.Printf("  updated: %s\n", orNone(tsString(rec.UpdatedAt)))
	if rec.Summary != "" {
		fmt.Printf("  summary (through seq %d, updated %s):\n    %s\n",
			rec.SummaryThroughSeq, orNone(tsString(rec.SummaryUpdatedAt)), collapse(rec.Summary))
	}
	fmt.Println()
	for _, m := range msgs {
		fmt.Println(messageLine(m, *trunc))
	}
	fmt.Printf("\n%d message(s).\n", len(msgs))
	return nil
}

func sessionSearch(args []string) error {
	fs := flag.NewFlagSet("session search", flag.ExitOnError)
	var opts offlineStore
	opts.bind(fs)
	channel := fs.String("channel", "", "restrict to one channel kind")
	user := fs.String("user", "", "restrict to conversations opened by this user id")
	limit := fs.Int("limit", 0, "cap conversations returned (0 = service default)")
	snippets := fs.Int("snippets", 0, "cap matching messages shown per conversation (0 = service default)")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("search text required")
	}
	query := strings.Join(fs.Args(), " ")

	store, _, err := opts.open()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// Same code path the agent's session_search tool drives, so what
	// an operator sees here is what the model would have found. A nil
	// raft is fine: search only reads.
	svc := memory.NewSessionService(nil, store, memory.SessionConfig{})
	hits, err := svc.SearchTranscripts(context.Background(), memory.SessionSearchQuery{
		Text:               query,
		Channel:            *channel,
		UserID:             *user,
		Limit:              *limit,
		SnippetsPerSession: *snippets,
	})
	if err != nil {
		return err
	}

	if *asJSON {
		out := make([]map[string]any, 0, len(hits))
		for _, h := range hits {
			sn := make([]map[string]any, 0, len(h.Snippets))
			for _, s := range h.Snippets {
				sn = append(sn, map[string]any{"seq": s.Seq, "role": s.Role, "text": s.Text})
			}
			out = append(out, map[string]any{
				"session":  sessionFields(h.Session),
				"matches":  h.Matches,
				"snippets": sn,
			})
		}
		return emitJSON(map[string]any{"query": query, "hits": out})
	}

	fmt.Printf("=== TRANSCRIPT SEARCH: %q ===\n", query)
	if len(hits) == 0 {
		fmt.Println("no matches")
		return nil
	}
	for _, h := range hits {
		title := h.Session.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("\n  %s [%s]  %d match(es)\n", title, h.Session.Id, h.Matches)
		for _, sn := range h.Snippets {
			fmt.Printf("    [#%d %s] %s\n", sn.Seq, sn.Role, collapse(sn.Text))
		}
	}
	return nil
}

// listSessions reads the session index, applying the CLI filters and
// sorting most-recently-updated first.
func listSessions(store *memory.Store, channel, user string) ([]*lobslawv1.SessionRecord, error) {
	svc := memory.NewSessionService(nil, store, memory.SessionConfig{})
	all, err := svc.List(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]*lobslawv1.SessionRecord, 0, len(all))
	for _, r := range all {
		if channel != "" && r.Channel != channel {
			continue
		}
		if user != "" && r.UserId != user {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return laterThan(out[i].UpdatedAt, out[j].UpdatedAt)
	})
	return out, nil
}

// loadMessages reads one conversation's transcript in sequence order.
// Message keys are "<session id>:<zero-padded seq>", so the thread is
// an ordered prefix scan rather than a decrypt of every message in the
// cluster.
func loadMessages(store *memory.Store, id string) ([]*lobslawv1.SessionMessage, error) {
	var out []*lobslawv1.SessionMessage
	err := store.ForEachPrefix(memory.BucketSessionMessages, id+":", func(key string, raw []byte) error {
		var m lobslawv1.SessionMessage
		if err := proto.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("unmarshal message %q: %w", key, err)
		}
		out = append(out, &m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func messageLine(m *lobslawv1.SessionMessage, trunc int) string {
	text := collapse(m.Content)
	if trunc > 0 {
		text = truncate(text, trunc)
	}
	line := fmt.Sprintf("  [%03d] %-9s %s", m.Seq, m.Role, text)
	for _, tc := range m.ToolCalls {
		line += " tool_call=" + tc.Name
	}
	if m.ToolCallId != "" {
		line += " tool_result_for=" + m.ToolCallId
	}
	return line
}

func messageFields(m *lobslawv1.SessionMessage, trunc int) map[string]any {
	content := m.Content
	if trunc > 0 {
		content = truncate(content, trunc)
	}
	calls := make([]map[string]any, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		calls = append(calls, map[string]any{"id": tc.Id, "name": tc.Name, "arguments": tc.Arguments})
	}
	return map[string]any{
		"seq":          m.Seq,
		"role":         m.Role,
		"content":      content,
		"tool_calls":   calls,
		"tool_call_id": m.ToolCallId,
		"turn_id":      m.TurnId,
		"timestamp":    tsString(m.Timestamp),
	}
}

func sessionFields(r *lobslawv1.SessionRecord) map[string]any {
	return map[string]any{
		"id":                  r.Id,
		"channel":             r.Channel,
		"channel_id":          r.ChannelId,
		"user_id":             r.UserId,
		"title":               r.Title,
		"first_seq":           r.FirstSeq,
		"next_seq":            r.NextSeq,
		"retained":            retained(r),
		"created_at":          tsString(r.CreatedAt),
		"updated_at":          tsString(r.UpdatedAt),
		"summary":             r.Summary,
		"summary_through_seq": r.SummaryThroughSeq,
		"summary_updated_at":  tsString(r.SummaryUpdatedAt),
	}
}

// retained is the live message count. Trimming advances FirstSeq, so
// the difference is what is still on disk rather than what was ever
// written.
func retained(r *lobslawv1.SessionRecord) uint64 {
	if r.NextSeq <= r.FirstSeq {
		return 0
	}
	return r.NextSeq - r.FirstSeq
}

func lastSeq(r *lobslawv1.SessionRecord) uint64 {
	if r.NextSeq == 0 {
		return 0
	}
	return r.NextSeq - 1
}
