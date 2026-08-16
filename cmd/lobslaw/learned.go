package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// "Show me everything it decided on its own" and "forget everything
// you taught yourself" are one scan and one operation each, because
// provenance is a location rather than a tag. That is the payoff of
// the separate store, and these are where an operator collects it.

const learnedUsage = `lobslaw learned — inspect and manage what the agent taught itself

The node must be STOPPED. These subcommands open state.db directly and
bbolt takes an exclusive lock on the file, so a running node makes every
one of them fail.

subcommands:
  list                 what the agent has written for itself
  archive <id>...      move artefacts out of the live set, recoverably
  discard              archive everything (except pinned artefacts)
  restore <id>...      bring archived artefacts back, as proposals

Nothing here deletes. Archived artefacts stay readable with
--archived — an agent that can silently erase evidence of what it
taught itself is the wrong default.

archive, discard and restore are DRY RUN unless --apply is given.`

func dispatchLearned(args []string) bool {
	idx := findSubcmd(args, "learned")
	if idx < 0 {
		return false
	}
	sub := args[idx+1:]
	if len(sub) == 0 {
		fmt.Fprintln(os.Stderr, learnedUsage)
		os.Exit(2)
	}

	var err error
	switch sub[0] {
	case "list":
		err = learnedList(sub[1:])
	case "archive":
		err = learnedArchive(sub[1:])
	case "discard":
		err = learnedDiscard(sub[1:])
	case "restore":
		err = learnedRestore(sub[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown learned subcommand %q\n\n%s\n", sub[0], learnedUsage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "learned %s: %v\n", sub[0], err)
		os.Exit(1)
	}
	return true
}

func learnedList(args []string) error {
	fs := flag.NewFlagSet("learned list", flag.ExitOnError)
	var store offlineStore
	store.bind(fs)
	archived := fs.Bool("archived", false, "read the archive instead of the live set")
	owner := fs.String("owner", "", "restrict to one principal")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, path, err := store.open()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	st := memory.NewOfflineSelfTaught(s)
	records, err := st.List(*archived, *owner)
	if err != nil {
		return err
	}

	if *asJSON {
		out := make([]map[string]any, 0, len(records))
		for _, r := range records {
			out = append(out, learnedJSON(r, st))
		}
		return emitJSON(map[string]any{"state_db": path, "artefacts": out})
	}

	fmt.Printf("%s\n", path)
	if len(records) == 0 {
		if *archived {
			fmt.Println("the archive is empty.")
		} else {
			fmt.Println("the agent has taught itself nothing.")
		}
		return nil
	}
	for _, r := range records {
		use := st.Usage(r.Id)
		pin := ""
		if r.Pinned {
			pin = " [pinned]"
		}
		fmt.Printf("  %-36s %-10s %-9s used %d%s\n",
			r.Id, kindLabel(r.Kind), stateLabel(r.State), use.Invocations, pin)
		if r.ArchivedReason != "" {
			fmt.Printf("      archived: %s\n", r.ArchivedReason)
		}
		if r.TurnId != "" {
			fmt.Printf("      taught by turn %s\n", r.TurnId)
		}
	}
	fmt.Printf("\n%d artefact(s).\n", len(records))
	return nil
}

func learnedArchive(args []string) error {
	return mutateLearned("learned archive", args, func(st *memory.OfflineSelfTaught, ids []string, apply bool) error {
		if len(ids) == 0 {
			return fmt.Errorf("name at least one artefact id (see: lobslaw learned list)")
		}
		return archiveIDs(st, ids, apply, "archived by operator")
	})
}

func learnedDiscard(args []string) error {
	return mutateLearned("learned discard", args, func(st *memory.OfflineSelfTaught, _ []string, apply bool) error {
		live, err := st.List(false, "")
		if err != nil {
			return err
		}
		var ids []string
		for _, r := range live {
			if r.Pinned {
				// Said out loud rather than silently skipped: an
				// operator running "discard" and finding something
				// still there deserves to know why.
				fmt.Printf("  SKIPPED %-30s (pinned)\n", r.Id)
				continue
			}
			ids = append(ids, r.Id)
		}
		return archiveIDs(st, ids, apply, "discarded by operator")
	})
}

func learnedRestore(args []string) error {
	return mutateLearned("learned restore", args, func(st *memory.OfflineSelfTaught, ids []string, apply bool) error {
		if len(ids) == 0 {
			return fmt.Errorf("name at least one artefact id (see: lobslaw learned list --archived)")
		}
		for _, id := range ids {
			rec, archived, err := st.Find(id)
			if err != nil {
				fmt.Printf("  NOT FOUND %s\n", id)
				continue
			}
			if !archived {
				fmt.Printf("  ALREADY LIVE %s\n", id)
				continue
			}
			fmt.Printf("  %-36s restore as proposed\n", id)
			if apply {
				if err := st.Restore(rec); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
			}
		}
		return nil
	})
}

func archiveIDs(st *memory.OfflineSelfTaught, ids []string, apply bool, reason string) error {
	for _, id := range ids {
		rec, archived, err := st.Find(id)
		if err != nil {
			fmt.Printf("  NOT FOUND %s\n", id)
			continue
		}
		if archived {
			fmt.Printf("  ALREADY ARCHIVED %s\n", id)
			continue
		}
		fmt.Printf("  %-36s archive\n", id)
		if apply {
			if err := st.Archive(rec, reason); err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
		}
	}
	return nil
}

func mutateLearned(name string, args []string, fn func(*memory.OfflineSelfTaught, []string, bool) error) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var store offlineStore
	store.bind(fs)
	apply := fs.Bool("apply", false, "actually write (default is a dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, path, err := store.open()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	fmt.Printf("%s\n", path)
	if err := fn(memory.NewOfflineSelfTaught(s), fs.Args(), *apply); err != nil {
		return err
	}
	if !*apply {
		fmt.Println("\nDRY RUN — nothing was written. Re-run with --apply.")
	}
	return nil
}

func learnedJSON(r *lobslawv1.SelfTaughtRecord, st *memory.OfflineSelfTaught) map[string]any {
	m := map[string]any{
		"id":      r.Id,
		"kind":    kindLabel(r.Kind),
		"name":    r.Name,
		"state":   stateLabel(r.State),
		"origin":  originLabel(r.Origin),
		"owner":   r.Owner,
		"pinned":  r.Pinned,
		"version": r.Version,
		"turn_id": r.TurnId,
		"uses":    st.Usage(r.Id).Invocations,
	}
	if r.ArchivedReason != "" {
		m["archived_reason"] = r.ArchivedReason
	}
	if r.CreatedAt != nil {
		m["created_at"] = r.CreatedAt.AsTime()
	}
	return m
}

func kindLabel(k lobslawv1.SelfTaughtKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "SELF_TAUGHT_KIND_"))
}

func stateLabel(s lobslawv1.SelfTaughtState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SELF_TAUGHT_STATE_"))
}

func originLabel(o lobslawv1.SelfTaughtOrigin) string {
	return strings.ToLower(strings.TrimPrefix(o.String(), "SELF_TAUGHT_ORIGIN_"))
}
