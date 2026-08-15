// Package main is a one-off inspector for the encrypted lobslaw
// state.db. Dumps episodic + vector records in JSON for operator
// debugging. Not part of the shipped binary — run via
// `go run ./cmd/inspect <path-to-state.db>`. LOBSLAW_MEMORY_KEY
// must be exported.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: inspect <path-to-state.db> [search-text]")
		os.Exit(1)
	}
	path := os.Args[1]
	keyRaw := os.Getenv("LOBSLAW_MEMORY_KEY")
	if keyRaw == "" {
		fmt.Fprintln(os.Stderr, "LOBSLAW_MEMORY_KEY env var required")
		os.Exit(1)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(keyRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode key:", err)
		os.Exit(1)
	}
	var key crypto.Key
	copy(key[:], keyBytes)

	store, err := memory.OpenStore(path, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	// bbolt fsyncs on every Update, so durability does not depend on
	// this Close; it only releases the file lock and mmap at exit.
	defer func() { _ = store.Close() }()

	// With a search argument, run a transcript search instead of the
	// full dump — the operator-side view of what session_search
	// gives the agent.
	if len(os.Args) > 2 {
		searchTranscripts(store, strings.Join(os.Args[2:], " "))
		return
	}

	fmt.Println("=== EPISODIC RECORDS ===")
	count := 0
	_ = store.ForEach(memory.BucketEpisodicRecords, func(_ string, v []byte) error {
		var r lobslawv1.EpisodicRecord
		if err := proto.Unmarshal(v, &r); err != nil {
			return nil
		}
		count++
		j, _ := json.MarshalIndent(map[string]any{
			"id":         r.Id,
			"event":      r.Event,
			"context":    r.Context,
			"importance": r.Importance,
			"tags":       r.Tags,
			"retention":  r.Retention,
			"timestamp":  r.Timestamp.AsTime().Format("2006-01-02 15:04:05 UTC"),
		}, "", "  ")
		fmt.Println(string(j))
		fmt.Println("---")
		return nil
	})
	fmt.Printf("\nTotal episodic records: %d\n\n", count)

	fmt.Println("=== VECTOR RECORDS ===")
	vcount := 0
	_ = store.ForEach(memory.BucketVectorRecords, func(_ string, v []byte) error {
		var r lobslawv1.VectorRecord
		if err := proto.Unmarshal(v, &r); err != nil {
			return nil
		}
		vcount++
		fmt.Printf("  id=%s scope=%s dims=%d source_ids=%v text_len=%d\n",
			r.Id, r.Scope, len(r.Embedding), r.SourceIds, len(r.Text))
		return nil
	})
	fmt.Printf("\nTotal vector records: %d\n\n", vcount)

	fmt.Println("=== SESSIONS ===")
	scount := 0
	_ = store.ForEach(memory.BucketSessions, func(_ string, v []byte) error {
		var r lobslawv1.SessionRecord
		if err := proto.Unmarshal(v, &r); err != nil {
			return nil
		}
		scount++
		fmt.Printf("\n  %s  user=%s  retained=%d (seq %d..%d)  updated=%s\n",
			r.Id, r.UserId, r.NextSeq-r.FirstSeq, r.FirstSeq, r.NextSeq-1,
			r.UpdatedAt.AsTime().Format("2006-01-02 15:04:05 UTC"))
		if r.Summary != "" {
			fmt.Printf("    summary (through seq %d): %s\n", r.SummaryThroughSeq, r.Summary)
		}
		// Message keys are "<session id>:<seq>", so the transcript is
		// an ordered prefix scan.
		_ = store.ForEachPrefix(memory.BucketSessionMessages, r.Id+":", func(_ string, mv []byte) error {
			var m lobslawv1.SessionMessage
			if err := proto.Unmarshal(mv, &m); err != nil {
				return nil
			}
			text := m.Content
			if len(text) > 100 {
				text = text[:100] + "…"
			}
			detail := ""
			for _, tc := range m.ToolCalls {
				detail += " tool_call=" + tc.Name
			}
			if m.ToolCallId != "" {
				detail += " tool_result_for=" + m.ToolCallId
			}
			fmt.Printf("    [%03d] %-9s %s%s\n", m.Seq, m.Role, text, detail)
			return nil
		})
		return nil
	})
	fmt.Printf("\nTotal sessions: %d\n", scount)
}

// searchTranscripts mirrors the agent's session_search tool so an
// operator can check what it would find without driving a turn.
func searchTranscripts(store *memory.Store, query string) {
	svc := memory.NewSessionService(nil, store, memory.SessionConfig{})
	hits, err := svc.SearchTranscripts(context.Background(), memory.SessionSearchQuery{Text: query})
	if err != nil {
		fmt.Fprintln(os.Stderr, "search:", err)
		os.Exit(1)
	}
	fmt.Printf("=== TRANSCRIPT SEARCH: %q ===\n", query)
	if len(hits) == 0 {
		fmt.Println("no matches")
		return
	}
	for _, h := range hits {
		title := h.Session.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("\n  %s [%s]  %d match(es)\n", title, h.Session.Id, h.Matches)
		for _, sn := range h.Snippets {
			text := strings.Join(strings.Fields(sn.Text), " ")
			fmt.Printf("    [#%d %s] %s\n", sn.Seq, sn.Role, text)
		}
	}
}
