package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/jmylchreest/lobslaw/internal/trace"
	"github.com/jmylchreest/lobslaw/pkg/config"
)

// Reading a turn back.
//
// Traces are per-node files, so this reads the local node's directory
// directly rather than talking to a cluster. That is the honest shape:
// a turn served on another node was traced on that node, and pretending
// otherwise would produce a command that silently returns nothing for
// half the turns you ask about.

const traceUsage = `lobslaw trace — what a turn did, and what it cost

  trace list [--limit N]     turns recorded on this node, newest first
  trace <turn-id>            the spans of one turn

Traces are per-node files under <data-dir>/traces. A turn served on
another node was traced there, not here.

Enable with:

  [trace]
  enabled = true

No span carries message text, tool arguments or tool output.`

func dispatchTrace(args []string) bool {
	idx := findSubcmd(args, "trace")
	if idx < 0 {
		return false
	}
	sub := args[idx+1:]
	if len(sub) == 0 {
		fmt.Fprintln(os.Stderr, traceUsage)
		os.Exit(2)
	}

	var err error
	if sub[0] == "list" {
		err = traceList(sub[1:])
	} else {
		err = traceShow(sub)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace: %v\n", err)
		os.Exit(1)
	}
	return true
}

// traceDir resolves where traces live, mirroring the node's own
// resolution so the CLI and the node cannot disagree about the path.
func traceDir(fs *flag.FlagSet) (*string, *string) {
	cfgPath := fs.String("config", envOr("LOBSLAW_CONFIG", ""),
		"path to config.toml; supplies [cluster] data_dir and [trace] dir")
	dir := fs.String("dir", envOr("LOBSLAW_TRACE_DIR", ""),
		"explicit trace directory; overrides --config")
	return cfgPath, dir
}

func resolveTraceDir(cfgPath, dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	if cfgPath == "" {
		return "", fmt.Errorf("no --dir and no --config to read one from")
	}
	cfg, err := config.Load(config.LoadOptions{Path: cfgPath})
	if err != nil {
		return "", fmt.Errorf("load config %q: %w", cfgPath, err)
	}
	if cfg.Trace.Dir != "" {
		return cfg.Trace.Dir, nil
	}
	if cfg.Cluster.DataDir == "" {
		return "", fmt.Errorf("config has neither [trace] dir nor [cluster] data_dir")
	}
	// "traces" duplicated from internal/node rather than exported: a
	// constant shared between a CLI and a server is a coupling that
	// outlives the reason for it, and the path is in the docs either
	// way.
	return filepath.Join(cfg.Cluster.DataDir, "traces"), nil
}

func traceList(args []string) error {
	fs := flag.NewFlagSet("trace list", flag.ExitOnError)
	cfgPath, dir := traceDir(fs)
	limit := fs.Int("limit", 20, "how many turns to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolved, err := resolveTraceDir(*cfgPath, *dir)
	if err != nil {
		return err
	}
	ids, err := trace.ListTurns(resolved, *limit)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		// Distinguished from an error. A node with tracing off and a
		// node that has served no turns both have nothing to show, and
		// neither is a failure.
		fmt.Printf("no turns recorded in %s\n", resolved)
		return nil
	}
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

func traceShow(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	cfgPath, dir := traceDir(fs)
	// The turn id is positional and comes first, so parse the rest.
	turnID := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	resolved, err := resolveTraceDir(*cfgPath, *dir)
	if err != nil {
		return err
	}
	spans, err := trace.ReadTurn(resolved, turnID)
	if err != nil {
		return err
	}
	if len(spans) == 0 {
		return fmt.Errorf("no spans for turn %q in %s — it may have been served on another node, "+
			"or rotated out", turnID, resolved)
	}
	sort.SliceStable(spans, func(i, j int) bool {
		return spans[i].StartedAt.Before(spans[j].StartedAt)
	})
	renderTurn(turnID, spans)
	return nil
}

// renderTurn prints one turn's spans with a total.
//
// The total is the point of the command. A list of spans answers "what
// happened"; the total answers "why did that cost what it did", which
// is the question somebody opened this for.
func renderTurn(turnID string, spans []trace.Span) {
	var totalCost, attributed float64
	var totalDur time.Duration
	var prompt, completion, cached int
	for _, s := range spans {
		totalDur += s.Duration
		// A context-carry span ATTRIBUTES cost the LLM spans have
		// already counted; it does not add any. Summing both would
		// double the turn's token count and roughly double its cost —
		// which would make the one command whose job is to answer "why
		// did this cost what it did" answer it wrongly.
		if s.Kind == trace.KindContextCarry {
			attributed += s.CostUSD
			continue
		}
		totalCost += s.CostUSD
		prompt += s.Usage.PromptTokens
		completion += s.Usage.CompletionTokens
		cached += s.Usage.CachedTokens
	}

	fmt.Printf("turn %s — %d spans, %s, $%.4f\n", turnID, len(spans),
		totalDur.Round(time.Millisecond), totalCost)
	if attributed > 0 {
		// Stated as a share of the total, not added to it. This is the
		// number R24 exists for: in an agentic turn it is usually the
		// dominant cost and is currently attributable to nothing.
		share := 0.0
		if totalCost > 0 {
			share = attributed / totalCost * 100
		}
		fmt.Printf("  of which $%.4f (%.0f%%) is re-sent tool output\n", attributed, share)
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KIND\tPROVIDER\tNAME\tTRY\tOUTCOME\tDURATION\tTOKENS\tCOST\tDETAIL")
	for _, s := range spans {
		tokens := ""
		if s.Usage.PromptTokens+s.Usage.CompletionTokens > 0 {
			tokens = fmt.Sprintf("%d/%d", s.Usage.PromptTokens, s.Usage.CompletionTokens)
			if s.Usage.CachedTokens > 0 {
				tokens += fmt.Sprintf(" (%d cached)", s.Usage.CachedTokens)
			}
		}
		if s.Unit != "" {
			// A non-token-billed call carries its own unit. A token
			// count of zero on a call that cost money reads as free.
			//
			// A context carry has both: the tokens it contributed and
			// the number of prompts it rode in. Showing only one hides
			// the calculation — "40020 tokens" looks like a big tool,
			// and "5 resends" looks like nothing at all.
			if s.Usage.PromptTokens > 0 {
				tokens = fmt.Sprintf("%d over %g %s", s.Usage.PromptTokens, s.Quantity, s.Unit)
			} else {
				tokens = fmt.Sprintf("%g %s", s.Quantity, s.Unit)
			}
		}
		cost := ""
		if s.CostUSD > 0 {
			cost = fmt.Sprintf("$%.4f", s.CostUSD)
		}
		dur := ""
		if s.Duration > 0 {
			dur = s.Duration.Round(time.Millisecond).String()
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			s.Kind, s.Provider, s.Name, s.Attempt, s.Outcome, dur, tokens, cost, s.Error)
	}
	_ = w.Flush()

	if cached > 0 {
		fmt.Printf("\ntokens: %d prompt (%d cached), %d completion\n", prompt, cached, completion)
	} else if prompt+completion > 0 {
		fmt.Printf("\ntokens: %d prompt, %d completion\n", prompt, completion)
	}
}
