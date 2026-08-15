package node_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/node"
	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The council tools were dead on every node for as long as they have
// existed: wireCompute guarded their registration on
// n.providerRegistry != nil, and that field is assigned by
// wireLLMProviders further down the same function. The guard read like
// a capability check and was really a read of a variable that had not
// been written yet.
//
// This asserts the property the guard was meant to express — more than
// one provider means the council tools exist — rather than asserting
// where in the function the call sits, which is what regressed.
func TestCouncilToolsRegisterWithMultipleProviders(t *testing.T) {
	// Not parallel: t.Setenv, which the provider's api_key_ref needs.
	t.Setenv("COUNCIL_TEST_KEY", "not-a-real-key")
	tmp := t.TempDir()
	nodeID := "council-node"
	creds := signNodeCert(t, filepath.Join(tmp, "certs"), nodeID)

	n, err := node.New(node.Config{
		NodeID:     nodeID,
		Functions:  []types.NodeFunction{types.FunctionCompute},
		ListenAddr: "127.0.0.1:0",
		Creds:      creds,
		Compute: config.ComputeConfig{
			Providers: []config.ProviderConfig{
				{Label: "primary", TrustTier: types.TrustPrivate, Endpoint: "https://example.invalid", APIKeyRef: "env:COUNCIL_TEST_KEY", Model: "m1"},
				{Label: "secondary", TrustTier: types.TrustPrivate, Endpoint: "https://example.invalid", APIKeyRef: "env:COUNCIL_TEST_KEY", Model: "m2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	t.Cleanup(func() { _ = n.Shutdown(context.Background()) })

	for _, want := range []string{"list_providers", "council_review"} {
		if _, ok := n.ToolRegistry().Get(want); !ok {
			t.Errorf("%s is not registered with two providers configured", want)
		}
	}
}

// The guard's other half still has to hold: one provider is not a
// council, and offering the model a review tool that can only ask
// itself is worse than not offering one.
func TestCouncilToolsAbsentWithSingleProvider(t *testing.T) {
	t.Setenv("COUNCIL_TEST_KEY", "not-a-real-key")
	tmp := t.TempDir()
	nodeID := "solo-node"
	creds := signNodeCert(t, filepath.Join(tmp, "certs"), nodeID)

	n, err := node.New(node.Config{
		NodeID:     nodeID,
		Functions:  []types.NodeFunction{types.FunctionCompute},
		ListenAddr: "127.0.0.1:0",
		Creds:      creds,
		Compute: config.ComputeConfig{
			Providers: []config.ProviderConfig{
				{Label: "only", TrustTier: types.TrustPrivate, Endpoint: "https://example.invalid", APIKeyRef: "env:COUNCIL_TEST_KEY", Model: "m1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}
	t.Cleanup(func() { _ = n.Shutdown(context.Background()) })

	if _, ok := n.ToolRegistry().Get("council_review"); ok {
		t.Error("council_review registered with a single provider")
	}
}
