package main

import (
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// "Revocable" is the justification for letting a user tap Always at
// all. If the CLI cannot separate the grants they made from the rules
// an operator wrote, it is not a revoke command, it is a policy wipe.

func policyTestStore(t *testing.T, rules ...*lobslawv1.PolicyRule) *memory.Store {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(t.TempDir(), "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, r := range rules {
		raw, err := proto.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(memory.BucketPolicyRules, r.Id, raw); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestApprovalMintedRulesSeparatesProvenance(t *testing.T) {
	t.Parallel()
	store := policyTestStore(t,
		&lobslawv1.PolicyRule{
			Id: "approval:p2", Subject: "user:alice", Action: "tool:exec",
			Resource: "send_email", Effect: "allow",
			CreatedBy: "approval:p2", CreatedAt: timestamppb.Now(),
		},
		&lobslawv1.PolicyRule{
			Id: "approval:p1", Subject: "user:alice", Action: "tool:exec",
			Resource: "write_file", Effect: "allow",
			CreatedBy: "approval:p1", CreatedAt: timestamppb.Now(),
		},
		&lobslawv1.PolicyRule{
			Id: "operator-allow-all", Subject: "*", Action: "*",
			Resource: "*", Effect: "allow",
		},
		&lobslawv1.PolicyRule{
			// Provenance from somewhere else entirely. Not ours to touch.
			Id: "seeded-stdlib", Subject: "*", Action: "tool:exec",
			Resource: "read_file", Effect: "allow", CreatedBy: "seed:stdlib",
		},
	)

	got, err := approvalMintedRules(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d approval rules, want 2: %+v", len(got), got)
	}
	// Sorted by id, so output is stable between runs.
	if got[0].Id != "approval:p1" || got[1].Id != "approval:p2" {
		t.Errorf("unsorted or wrong rules: %s, %s", got[0].Id, got[1].Id)
	}
	for _, r := range got {
		if r.CreatedBy == "seed:stdlib" || r.Id == "operator-allow-all" {
			t.Errorf("a rule nobody approved was listed as an approval: %+v", r)
		}
	}
}

func TestApprovalRuleJSONCarriesProvenance(t *testing.T) {
	t.Parallel()
	when := timestamppb.Now()
	m := approvalRuleJSON(&lobslawv1.PolicyRule{
		Id: "approval:p1", Subject: "user:alice", Action: "tool:exec",
		Resource: "write_file", Effect: "allow",
		CreatedBy: "approval:p1", CreatedAt: when,
	})
	if m["created_by"] != "approval:p1" {
		t.Errorf("created_by = %v; without it the caller cannot tell an approval from an operator rule", m["created_by"])
	}
	if m["created_at"] == nil {
		t.Error("no created_at; an operator reviewing grants cannot tell when this happened")
	}
	if m["effect"] != "allow" {
		t.Errorf("effect = %v, want allow", m["effect"])
	}
}
