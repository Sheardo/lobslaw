package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/policy"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// An "always" approval is a permanent widening of what the agent may
// do, tapped once and then easy to forget. These subcommands are the
// other half of that feature: without a way to see and undo the
// grants, "revocable" is a claim in a doc rather than something a
// person can act on.

const policyUsage = `lobslaw policy — inspect and revoke policy rules offline

The node must be STOPPED. These subcommands open state.db directly and
bbolt takes an exclusive lock on the file, so a running node makes every
one of them fail.

subcommands:
  approvals          list the rules minted by "always" approvals
  revoke-approvals   delete them, all or by id

revoke-approvals is DRY RUN unless --apply is given, and refuses to
touch any rule an operator wrote — only approval-minted ones.`

// dispatchPolicy handles `lobslaw policy <subcmd>`.
func dispatchPolicy(args []string) bool {
	idx := findSubcmd(args, "policy")
	if idx < 0 {
		return false
	}
	sub := args[idx+1:]
	if len(sub) == 0 {
		fmt.Fprintln(os.Stderr, policyUsage)
		os.Exit(2)
	}

	var err error
	switch sub[0] {
	case "approvals":
		err = policyApprovals(sub[1:])
	case "revoke-approvals":
		err = policyRevokeApprovals(sub[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown policy subcommand %q\n\n%s\n", sub[0], policyUsage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy %s: %v\n", sub[0], err)
		os.Exit(1)
	}
	return true
}

func policyApprovals(args []string) error {
	fs := flag.NewFlagSet("policy approvals", flag.ExitOnError)
	var store offlineStore
	store.bind(fs)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, path, err := store.open()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rules, err := approvalMintedRules(s)
	if err != nil {
		return err
	}

	if *asJSON {
		out := make([]map[string]any, 0, len(rules))
		for _, r := range rules {
			out = append(out, approvalRuleJSON(r))
		}
		return emitJSON(map[string]any{"state_db": path, "rules": out})
	}

	fmt.Printf("%s\n", path)
	if len(rules) == 0 {
		fmt.Println("no approval-minted rules.")
		return nil
	}
	for _, r := range rules {
		when := ""
		if r.CreatedAt != nil {
			when = r.CreatedAt.AsTime().Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-40s %s %s -> %s  (%s)\n",
			r.Id, r.Subject, r.Action, r.Resource, when)
	}
	fmt.Printf("\n%d rule(s). Revoke with: lobslaw policy revoke-approvals [<id>...] --apply\n", len(rules))
	return nil
}

func policyRevokeApprovals(args []string) error {
	fs := flag.NewFlagSet("policy revoke-approvals", flag.ExitOnError)
	var store offlineStore
	store.bind(fs)
	apply := fs.Bool("apply", false, "actually delete (default is a dry run)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wanted := fs.Args()

	s, path, err := store.open()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rules, err := approvalMintedRules(s)
	if err != nil {
		return err
	}

	// An id the operator named that is not an approval-minted rule is
	// reported rather than skipped. Silently ignoring it would let
	// someone believe they had revoked something they had not.
	var target []*lobslawv1.PolicyRule
	var unknown []string
	if len(wanted) == 0 {
		target = rules
	} else {
		byID := make(map[string]*lobslawv1.PolicyRule, len(rules))
		for _, r := range rules {
			byID[r.Id] = r
		}
		for _, id := range wanted {
			if r, ok := byID[id]; ok {
				target = append(target, r)
				continue
			}
			unknown = append(unknown, id)
		}
	}

	if *apply {
		for _, r := range target {
			if err := s.Delete(memory.BucketPolicyRules, r.Id); err != nil {
				return fmt.Errorf("delete %s: %w", r.Id, err)
			}
		}
	}

	if *asJSON {
		ids := make([]string, 0, len(target))
		for _, r := range target {
			ids = append(ids, r.Id)
		}
		return emitJSON(map[string]any{
			"applied":  *apply,
			"state_db": path,
			"revoked":  ids,
			"unknown":  unknown,
		})
	}

	fmt.Printf("%s\n", path)
	for _, r := range target {
		fmt.Printf("  %-40s %s %s -> %s\n", r.Id, r.Subject, r.Action, r.Resource)
	}
	if len(unknown) > 0 {
		fmt.Printf("\nnot approval-minted (left alone): %s\n", strings.Join(unknown, ", "))
	}
	switch {
	case len(target) == 0:
		fmt.Println("\nnothing to do.")
	case *apply:
		fmt.Printf("\nREVOKED %d rule(s).\n", len(target))
	default:
		fmt.Printf("\nDRY RUN — nothing was written. Re-run with --apply to revoke %d rule(s).\n", len(target))
	}
	return nil
}

// approvalMintedRules reads every rule whose provenance says an
// approval created it, sorted by id so output is stable.
func approvalMintedRules(s *memory.Store) ([]*lobslawv1.PolicyRule, error) {
	var out []*lobslawv1.PolicyRule
	err := s.ForEach(memory.BucketPolicyRules, func(_ string, raw []byte) error {
		var rule lobslawv1.PolicyRule
		if err := proto.Unmarshal(raw, &rule); err != nil {
			return nil //nolint:nilerr // one unreadable rule should not hide the rest
		}
		if strings.HasPrefix(rule.CreatedBy, policy.ApprovalRulePrefix) {
			out = append(out, &rule)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read policy rules: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

func approvalRuleJSON(r *lobslawv1.PolicyRule) map[string]any {
	m := map[string]any{
		"id":         r.Id,
		"subject":    r.Subject,
		"action":     r.Action,
		"resource":   r.Resource,
		"effect":     r.Effect,
		"created_by": r.CreatedBy,
	}
	if r.CreatedAt != nil {
		m["created_at"] = r.CreatedAt.AsTime()
	}
	return m
}
