package policy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

func newApprovalRules(t *testing.T) (*ApprovalRules, *memory.Store) {
	t.Helper()
	dataDir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(dataDir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	_, inmem := raft.NewInmemTransport("approval-node")
	node, err := memory.NewRaft(memory.RaftConfig{
		NodeID: "approval-node", LocalAddr: "approval-node",
		DataDir: dataDir, Bootstrap: true, Transport: inmem,
	}, memory.NewFSM(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Shutdown()
		_ = store.Close()
	})
	if err := node.WaitForLeader(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	a, err := NewApprovalRules(node, store)
	if err != nil {
		t.Fatal(err)
	}
	return a, store
}

// The point of "always" being a policy rule rather than a second
// store: the engine that already answers this question keeps
// answering it.
func TestMintedRuleActuallyGrants(t *testing.T) {
	t.Parallel()
	a, store := newApprovalRules(t)

	if _, err := a.Mint(context.Background(), MintRequest{
		PromptID: "p1", Subject: "user:alice",
		Action: "tool:exec", Resource: "write_file",
	}); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, nil)
	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "tool:exec", "write_file")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != "allow" {
		t.Errorf("the minted rule produced %q, want allow — the approval did nothing", dec.Effect)
	}
}

// The whole risk of a permanent grant is that the user forgets they
// gave it. Provenance is what makes it findable and revocable.
func TestMintedRuleIsFindableAndRevocable(t *testing.T) {
	t.Parallel()
	a, store := newApprovalRules(t)

	// An operator-authored rule that must survive everything below.
	operatorRule := &lobslawv1.PolicyRule{
		Id: "operator-rule", Subject: "*", Action: "memory:read",
		Resource: "*", Effect: "allow", Priority: 5,
	}
	raw, _ := proto.Marshal(operatorRule)
	if err := store.Put(memory.BucketPolicyRules, operatorRule.Id, raw); err != nil {
		t.Fatal(err)
	}

	minted, err := a.Mint(context.Background(), MintRequest{
		PromptID: "p7", Subject: "user:alice",
		Action: "tool:exec", Resource: "send_email",
	})
	if err != nil {
		t.Fatal(err)
	}
	if minted.CreatedBy != "approval:p7" {
		t.Errorf("created_by = %q, want approval:p7", minted.CreatedBy)
	}
	if minted.CreatedAt == nil {
		t.Error("no created_at; an operator reviewing grants cannot tell when this happened")
	}

	found, err := a.FromApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Id != minted.Id {
		t.Fatalf("FromApprovals returned %d rules, want just the minted one: %+v", len(found), found)
	}

	if err := a.Revoke(minted.Id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := store.Get(memory.BucketPolicyRules, minted.Id); err == nil {
		t.Error("the rule survived revocation")
	}
	if _, err := store.Get(memory.BucketPolicyRules, operatorRule.Id); err != nil {
		t.Error("revoking an approval removed an operator-authored rule")
	}
}

// "Revoke my approvals" must not become a way to delete rules
// somebody wrote on purpose.
func TestRevokeRefusesOperatorRules(t *testing.T) {
	t.Parallel()
	a, store := newApprovalRules(t)

	rule := &lobslawv1.PolicyRule{
		Id: "operator-rule", Subject: "*", Action: "*", Resource: "*", Effect: "allow",
	}
	raw, _ := proto.Marshal(rule)
	if err := store.Put(memory.BucketPolicyRules, rule.Id, raw); err != nil {
		t.Fatal(err)
	}

	if err := a.Revoke(rule.Id); err == nil {
		t.Fatal("an operator-authored rule was revoked as though it were an approval")
	}
	if _, err := store.Get(memory.BucketPolicyRules, rule.Id); err != nil {
		t.Error("the rule was deleted despite the refusal")
	}
}

// The roadmap asks for this specifically, and asks for it as a test
// rather than as an argument about call ordering.
func TestAlwaysCannotGrantPastTheHardlineFloor(t *testing.T) {
	t.Parallel()
	a, _ := newApprovalRules(t)
	home := t.TempDir()

	// Distinct prompt ids, so the store-count assertion below counts
	// seven refusals rather than one id overwritten seven times.
	for i, resource := range []string{
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".aws", "credentials"),
		"/etc/shadow",
		"/srv/app/.env",
		"/var/lib/lobslaw/state.db",
		"rm -rf /",
		":(){:|:&};:",
	} {
		rule, err := a.Mint(context.Background(), MintRequest{
			PromptID: fmt.Sprintf("p%d", i), Subject: "user:alice",
			Action: "tool:exec", Resource: resource,
		})
		if err == nil {
			t.Errorf("minted a permanent grant for %q: %+v", resource, rule)
			continue
		}
		if !errors.Is(err, ErrHardlineRule) {
			t.Errorf("%q was refused by %v, want ErrHardlineRule", resource, err)
		}
	}

	// And nothing reached the store, so a rule listing never shows an
	// operator a grant that reads as though it works.
	found, err := a.FromApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("%d rules were written despite every mint being refused: %+v", len(found), found)
	}
}

// A grant for "everything of this kind" is not what the button
// offered.
func TestMintRefusesWildcards(t *testing.T) {
	t.Parallel()
	a, _ := newApprovalRules(t)

	for _, req := range []MintRequest{
		{PromptID: "p", Subject: "user:alice", Action: "*", Resource: "write_file"},
		{PromptID: "p", Subject: "user:alice", Action: "tool:exec", Resource: "*"},
		{PromptID: "p", Subject: "user:alice", Action: "tool:*", Resource: "write_file"},
	} {
		if _, err := a.Mint(context.Background(), req); err == nil {
			t.Errorf("minted a wildcard grant: %+v", req)
		}
	}
}

// An empty subject matches everyone, which is the opposite of a grant
// scoped to the conversation the user was looking at.
func TestMintRequiresEveryField(t *testing.T) {
	t.Parallel()
	a, _ := newApprovalRules(t)

	full := MintRequest{
		PromptID: "p", Subject: "user:alice",
		Action: "tool:exec", Resource: "write_file",
	}
	for name, mutate := range map[string]func(*MintRequest){
		"prompt id": func(r *MintRequest) { r.PromptID = "" },
		"subject":   func(r *MintRequest) { r.Subject = "" },
		"action":    func(r *MintRequest) { r.Action = "" },
		"resource":  func(r *MintRequest) { r.Resource = "" },
	} {
		req := full
		mutate(&req)
		if _, err := a.Mint(context.Background(), req); err == nil {
			t.Errorf("minted a rule with no %s", name)
		}
	}
}

// A subject the engine cannot match fails closed, so minting one
// writes a rule that reads as a grant in a listing and grants nothing.
// Refused at mint time rather than discovered later.
func TestMintRefusesASubjectTheEngineCannotMatch(t *testing.T) {
	t.Parallel()
	a, _ := newApprovalRules(t)

	for _, subject := range []string{
		"telegram:-100", // a conversation, not a principal
		"alice",         // no kind at all
		"user:",         // kind with nothing after it
		"*",             // everyone
	} {
		if _, err := a.Mint(context.Background(), MintRequest{
			PromptID: "p", Subject: subject,
			Action: "tool:exec", Resource: "write_file",
		}); err == nil {
			t.Errorf("minted a rule for subject %q, which the engine will never match", subject)
		}
	}
}

// Re-tapping the same prompt must not pile up duplicate rules.
func TestMintIsIdempotentPerPrompt(t *testing.T) {
	t.Parallel()
	a, _ := newApprovalRules(t)

	req := MintRequest{
		PromptID: "p3", Subject: "user:alice",
		Action: "tool:exec", Resource: "write_file",
	}
	for range 3 {
		if _, err := a.Mint(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	found, err := a.FromApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Errorf("three taps produced %d rules, want 1", len(found))
	}
}

// An approval is the user answering one prompt. It should not outrank
// a rule somebody wrote deliberately.
func TestMintedRuleLosesToAnOperatorDeny(t *testing.T) {
	t.Parallel()
	a, store := newApprovalRules(t)

	deny := &lobslawv1.PolicyRule{
		Id: "operator-deny", Subject: "*", Action: "tool:exec",
		Resource: "send_email", Effect: "deny", Priority: 10,
	}
	raw, _ := proto.Marshal(deny)
	if err := store.Put(memory.BucketPolicyRules, deny.Id, raw); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Mint(context.Background(), MintRequest{
		PromptID: "p9", Subject: "user:alice",
		Action: "tool:exec", Resource: "send_email",
	}); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(store, nil)
	dec, err := eng.Evaluate(context.Background(),
		&types.Claims{UserID: "alice"}, "tool:exec", "send_email")
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect == "allow" {
		t.Errorf("an approval overrode an operator deny: %+v", dec)
	}
	if dec.Effect != types.EffectDeny {
		t.Errorf("effect = %q, want the operator's deny to stand", dec.Effect)
	}
}
