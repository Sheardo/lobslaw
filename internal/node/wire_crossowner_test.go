package node

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/audit"
	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/policy"
	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

func crossOwnerTestStore(t *testing.T) *memory.Store {
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
	return store
}

func seedRule(t *testing.T, store *memory.Store, r *lobslawv1.PolicyRule) {
	t.Helper()
	raw, err := proto.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(memory.BucketPolicyRules, r.Id, raw); err != nil {
		t.Fatal(err)
	}
}

// operatorGrant is the rule an operator writes to give themselves
// cross-owner read. Nothing seeds it, which is the point of the
// no-rule half of the table below.
func operatorGrant(effect string) *lobslawv1.PolicyRule {
	return &lobslawv1.PolicyRule{
		Id:       "operators-read-any-memory",
		Subject:  "role:operator",
		Action:   CrossOwnerAction,
		Resource: CrossOwnerResource,
		Effect:   effect,
		Priority: 50,
	}
}

func newCrossOwnerAuthorizer(t *testing.T, store *memory.Store, log *audit.AuditLog) *crossOwnerAuthorizer {
	t.Helper()
	return &crossOwnerAuthorizer{
		engine: policy.NewEngine(store, slog.Default()),
		audit:  log,
		log:    slog.Default(),
	}
}

// TestCrossOwnerAuthorizerIsTheRuleNotTheRole runs the real policy
// engine over the real rule store. The role is held in every row; only
// the rule changes.
func TestCrossOwnerAuthorizerIsTheRuleNotTheRole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		rule  *lobslawv1.PolicyRule
		roles []string
		want  bool
	}{
		{
			name:  "operator with a grant",
			rule:  operatorGrant(string(types.EffectAllow)),
			roles: []string{"operator"},
			want:  true,
		},
		{
			name:  "operator with no rule at all",
			roles: []string{"operator"},
			want:  false,
		},
		{
			name:  "operator the rule explicitly denies",
			rule:  operatorGrant(string(types.EffectDeny)),
			roles: []string{"operator"},
			want:  false,
		},
		{
			// An operator who wants their own reads gated has no user
			// to confirm with on the passive-recall path, so the only
			// safe reading of require_confirmation there is "no".
			name:  "operator the rule gates behind confirmation",
			rule:  operatorGrant(string(types.EffectRequireConfirmation)),
			roles: []string{"operator"},
			want:  false,
		},
		{
			name: "grant exists but the caller does not hold the role",
			rule: operatorGrant(string(types.EffectAllow)),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := crossOwnerTestStore(t)
			if tc.rule != nil {
				seedRule(t, store, tc.rule)
			}
			a := newCrossOwnerAuthorizer(t, store, nil)
			claims := &types.Claims{UserID: "alice", Scope: "default", Roles: tc.roles}
			if got := a.AllowsAny(context.Background(), claims); got != tc.want {
				t.Errorf("AllowsAny = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestCrossOwnerAuthorizerRefusesNilClaims(t *testing.T) {
	t.Parallel()
	store := crossOwnerTestStore(t)
	seedRule(t, store, operatorGrant(string(types.EffectAllow)))
	if newCrossOwnerAuthorizer(t, store, nil).AllowsAny(context.Background(), nil) {
		t.Error("a caller with no claims must not be widened")
	}
}

// TestCrossOwnerWideningIsAudited is the requirement the whole role
// exists to satisfy: reading another person's data leaves a record in
// the hash-chained log naming who did it.
func TestCrossOwnerWideningIsAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink, err := audit.NewLocalSink(audit.LocalConfig{
		Path: filepath.Join(t.TempDir(), "audit", "audit.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.NewAuditLog(ctx, audit.Config{Sinks: []audit.AuditSink{sink}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })

	store := crossOwnerTestStore(t)
	seedRule(t, store, operatorGrant(string(types.EffectAllow)))
	a := newCrossOwnerAuthorizer(t, store, log)

	if !a.AllowsAny(ctx, &types.Claims{UserID: "alice", Scope: "default", Roles: []string{"operator"}}) {
		t.Fatal("granted operator should be widened")
	}
	// A caller policy refuses is not a widening and must not be
	// recorded as one — an audit log padded with non-events is one
	// nobody reads.
	a.AllowsAny(ctx, &types.Claims{UserID: "mallory", Scope: "default"})

	entries, err := log.Query(ctx, "", types.AuditFilter{Action: CrossOwnerAction})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly one widening entry; got %d (%+v)", len(entries), entries)
	}
	e := entries[0]
	if e.ActorScope != "default:alice" {
		t.Errorf("actor = %q; want default:alice", e.ActorScope)
	}
	if e.PolicyRule != "operators-read-any-memory" {
		t.Errorf("policy_rule = %q; want the granting rule id", e.PolicyRule)
	}
	if e.Effect != types.EffectAllow {
		t.Errorf("effect = %q; want allow", e.Effect)
	}
}

// TestCrossOwnerAuthzNilWithoutPolicyEngine keeps the interface value
// nil rather than a typed nil pointer, so compute's "nil means no
// widening" default survives a node with no policy function.
func TestCrossOwnerAuthzNilWithoutPolicyEngine(t *testing.T) {
	t.Parallel()
	n := &Node{log: slog.Default()}
	if got := n.crossOwnerAuthz(); got != nil {
		t.Errorf("authorizer = %v; want an untyped nil interface", got)
	}
}

// TestResolveUserRolesFromConfig covers the declaration half: a
// channel with no token still reaches the policy engine as a subject a
// role rule can match.
func TestResolveUserRolesFromConfig(t *testing.T) {
	t.Parallel()
	n := &Node{log: slog.Default()}
	n.cfg.Users = []config.UserConfig{
		{ID: "alice", Roles: []string{"operator"}},
		{ID: "bob"},
	}
	n.cfg.Identity.Aliases = map[string]string{"tg-@alice": "alice"}

	for _, tc := range []struct {
		name   string
		userID string
		want   []string
	}{
		{name: "telegram id resolved through the alias map", userID: "tg-@alice", want: []string{"operator"}},
		{name: "canonical id declared directly", userID: "alice", want: []string{"operator"}},
		{name: "declared user with no roles", userID: "bob"},
		{name: "user nobody declared", userID: "tg-@carol"},
		{name: "anonymous turn", userID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := n.resolveUserRoles(tc.userID)
			if len(got) != len(tc.want) {
				t.Fatalf("roles = %v; want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("roles = %v; want %v", got, tc.want)
				}
			}
		})
	}
}
