package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// "Approved for the rest of this conversation" was an in-process map.
// The argument for that covered restarts and never covered the
// cluster: the user answered in one conversation and was asked again
// because the next message landed on a different node.

func grants(t *testing.T, ttl time.Duration) *SessionGrantStore {
	t.Helper()
	node, fsm := newTestRaft(t)
	s, err := NewSessionGrantStore(node, fsm.Store(), ttl)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustGrant(t *testing.T, s *SessionGrantStore, session, action, resource string) *lobslawv1.SessionGrant {
	t.Helper()
	g, err := s.Grant(context.Background(), GrantRequest{
		SessionID: session, Action: action, Resource: resource,
		GrantedBy: "user:alice", PromptID: "prompt-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAGrantIsHonouredAfterwards(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")

	if !s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("the grant was not honoured")
	}
	// And covers only what was asked about.
	if s.Granted("telegram:42", "tool:exec", "http") {
		t.Error("a grant for one resource covered another")
	}
	if s.Granted("telegram:42", "tool:write", "shell") {
		t.Error("a grant for one action covered another")
	}
}

// The whole point: a grant recorded through one store is visible to a
// second one reading the same replicated state. That is node B.
func TestAGrantIsVisibleToAnotherReader(t *testing.T) {
	t.Parallel()
	node, fsm := newTestRaft(t)
	nodeA, err := NewSessionGrantStore(node, fsm.Store(), 0)
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := NewSessionGrantStore(node, fsm.Store(), 0)
	if err != nil {
		t.Fatal(err)
	}

	mustGrant(t, nodeA, "telegram:42", "tool:exec", "shell")
	if !nodeB.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("a grant given on one node was invisible to another")
	}
}

// The confirmation was shown in a conversation and answered there. In
// a group chat the person who taps Approve is approving for that chat,
// so widening it to every conversation would grant more than the
// button appeared to offer.
func TestAGrantDoesNotLeakToAnotherConversation(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")

	if s.Granted("telegram:99", "tool:exec", "shell") {
		t.Error("a grant reached a different conversation")
	}
	if s.Granted("rest:42", "tool:exec", "shell") {
		t.Error("a grant reached the same id on a different channel")
	}
}

// --- the bound -----------------------------------------------------

// The process exiting used to be the bound, which made the lifetime of
// a security grant a function of deploy cadence. A stated TTL is a
// decision; that was not.
func TestAGrantExpires(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Millisecond)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	time.Sleep(5 * time.Millisecond)

	if s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("an expired grant was honoured")
	}
}

// Expiry is enforced on read rather than trusted to the sweeper. A
// grant revoked only when a background pass gets round to it is live
// for however long that pass is behind, and "how stale is the sweeper"
// must not be a question a permission check has an answer to.
func TestExpiryDoesNotDependOnTheSweeper(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Millisecond)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	time.Sleep(5 * time.Millisecond)

	// The record is still physically present — nothing has swept.
	if _, err := s.store.Get(BucketSessionGrants, GrantKey("telegram:42", "tool:exec", "shell")); err != nil {
		t.Fatalf("the record was already gone, so this proves nothing: %v", err)
	}
	if s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("an expired but unswept grant was honoured")
	}
}

// A grant with no expiry is treated as expired, not as eternal. The
// field is written on every path that creates one, so a record without
// it was not written by this code — and the safe reading of "I do not
// know when this stops" is that it already has.
func TestAGrantWithNoExpiryIsNotHonoured(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	key := GrantKey("telegram:42", "tool:exec", "shell")
	raw, err := proto.Marshal(&lobslawv1.SessionGrant{
		Id: key, SessionId: "telegram:42", Action: "tool:exec", Resource: "shell",
		GrantedAt: timestamppb.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Put(BucketSessionGrants, key, raw); err != nil {
		t.Fatal(err)
	}
	if s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("a grant with no expiry was honoured forever")
	}
}

// --- refusals ------------------------------------------------------

// An empty session id produces a grant keyed on nothing, which matches
// every conversation — the opposite of what the button offered.
func TestAGrantWithNoConversationIsRefused(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	for _, req := range []GrantRequest{
		{Action: "tool:exec", Resource: "shell"},
		{SessionID: "telegram:42", Resource: "shell"},
		{SessionID: "telegram:42", Action: "tool:exec"},
	} {
		if _, err := s.Grant(context.Background(), req); err == nil {
			t.Errorf("%+v was accepted", req)
		}
	}
}

// A wildcard turns "yes, this file" into "yes, every file", which is
// more than the confirmation described.
func TestAWildcardGrantIsRefused(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	for _, req := range []GrantRequest{
		{SessionID: "telegram:42", Action: "tool:*", Resource: "shell"},
		{SessionID: "telegram:42", Action: "tool:exec", Resource: "*"},
	} {
		_, err := s.Grant(context.Background(), req)
		if err == nil {
			t.Errorf("%+v was accepted", req)
			continue
		}
		if !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("err = %q; it does not say why", err)
		}
	}
}

// NUL separates the parts so a channel id containing the separator
// cannot forge a key belonging to a different conversation. It matters
// more now the key is replicated than it did in a process-local map.
func TestAConversationIdCannotForgeAnotherKey(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	// A conversation whose id, if the separator were ":", would read
	// as telegram:42 approving tool:exec on shell.
	mustGrant(t, s, "telegram:42:tool:exec", "x", "y")
	if s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("a crafted conversation id forged a grant for another conversation")
	}
}

// --- revocation ----------------------------------------------------

// The in-process version carried a NOTE asking for this and had no way
// to provide it: a cleared conversation must not keep privileges the
// user believes they revoked.
func TestForgettingAConversationDropsItsGrants(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	mustGrant(t, s, "telegram:42", "tool:exec", "http")
	mustGrant(t, s, "telegram:99", "tool:exec", "shell")

	n, err := s.RevokeSession(context.Background(), "telegram:42")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("revoked %d, want 2", n)
	}
	if s.Granted("telegram:42", "tool:exec", "shell") || s.Granted("telegram:42", "tool:exec", "http") {
		t.Error("a forgotten conversation kept a grant")
	}
	if !s.Granted("telegram:99", "tool:exec", "shell") {
		t.Error("forgetting one conversation revoked another's grant")
	}
}

// Including the expired ones. Leaving them behind would make "forget
// this conversation" a statement about what is enforceable rather than
// about what is stored.
func TestForgettingAConversationRemovesExpiredGrantsToo(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Millisecond)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	time.Sleep(5 * time.Millisecond)

	if _, err := s.RevokeSession(context.Background(), "telegram:42"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Get(BucketSessionGrants, GrantKey("telegram:42", "tool:exec", "shell")); err == nil {
		t.Error("an expired grant survived forgetting the conversation")
	}
}

func TestRevokeOneGrant(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	g := mustGrant(t, s, "telegram:42", "tool:exec", "shell")

	if err := s.Revoke(context.Background(), g.Id); err != nil {
		t.Fatal(err)
	}
	if s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("a revoked grant was honoured")
	}
	if err := s.Revoke(context.Background(), g.Id); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("err = %v, want ErrGrantNotFound", err)
	}
}

// --- listing and sweeping ------------------------------------------

// A standing grant nobody can see is one nobody can revoke, so it has
// to be listable with who gave it.
func TestGrantsAreListableWithProvenance(t *testing.T) {
	t.Parallel()
	s := grants(t, 0)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("listed %d", len(all))
	}
	if all[0].GrantedBy != "user:alice" || all[0].PromptId != "prompt-1" {
		t.Errorf("grant = %+v; provenance is missing", all[0])
	}
}

func TestListingHidesExpiredGrants(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Millisecond)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	time.Sleep(5 * time.Millisecond)

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("an expired grant was listed as live: %+v", all)
	}
}

func TestSweepRemovesOnlyExpiredGrants(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Millisecond)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")
	time.Sleep(5 * time.Millisecond)

	long := grants(t, time.Hour)
	_ = long

	n, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	if _, err := s.store.Get(BucketSessionGrants, GrantKey("telegram:42", "tool:exec", "shell")); err == nil {
		t.Error("the expired grant is still stored")
	}
}

func TestSweepKeepsLiveGrants(t *testing.T) {
	t.Parallel()
	s := grants(t, time.Hour)
	mustGrant(t, s, "telegram:42", "tool:exec", "shell")

	n, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("swept %d live grants", n)
	}
	if !s.Granted("telegram:42", "tool:exec", "shell") {
		t.Error("the sweep removed a live grant")
	}
}

func TestZeroTTLTakesTheDefault(t *testing.T) {
	t.Parallel()
	if got := grants(t, 0).TTL(); got != DefaultSessionGrantTTL {
		t.Errorf("ttl = %v, want %v", got, DefaultSessionGrantTTL)
	}
}
