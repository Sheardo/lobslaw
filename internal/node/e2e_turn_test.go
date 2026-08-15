package node_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/node"
	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Every test in this tree exercises one package. Nothing drove a
// message in and checked what came out, and that is the class of bug
// this session kept producing: the turn gate correct but not on the
// request path, the session leaser built but not attached,
// require_confirmation reaching the model instead of the user. Each
// was invisible to unit tests and obvious to one turn.
//
// So: boot a real node from real config, send a real message over the
// real HTTP surface, and assert on the reply and the durable
// transcript.
//
// It touches no network. Every provider is `driver = "mock"`, which is
// the point of the mock being a driver rather than a test-only
// injection — the config path is exercised too. A harness that reached
// the network would be an integration test for someone else's uptime.

func TestEndToEndTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end turn in short mode")
	}
	h := bootMockNode(t)

	first := h.send(t, "hello there", "sess-1")
	if first.Reply == "" {
		t.Fatal("the turn produced no reply")
	}
	// The mock names the model it was configured with, so this also
	// proves the reply came from the PROVIDER CONFIG rather than from
	// an injected default — the config path is what selected it.
	if !contains(first.Reply, "mock-main") {
		t.Errorf("reply = %q; the configured provider did not answer", first.Reply)
	}

	firstSaw := messagesSeen(t, first.Reply)

	// A second turn on the same session must see the first one. That
	// property spans the gateway, the session service, raft and the
	// store — and it is asserted here through the HTTP surface alone,
	// by asking the mock how much history reached it, rather than by
	// reaching into the store and asserting on its internals.
	second := h.send(t, "and again", "sess-1")
	secondSaw := messagesSeen(t, second.Reply)
	if secondSaw <= firstSaw {
		t.Errorf("second turn saw %d messages, first saw %d — the transcript did not round-trip",
			secondSaw, firstSaw)
	}

	// A DIFFERENT session must not inherit it. Getting this wrong is
	// how one user's conversation leaks into another's prompt.
	other := h.send(t, "unrelated", "sess-2")
	if got := messagesSeen(t, other.Reply); got != firstSaw {
		t.Errorf("a fresh session saw %d messages, want %d — history leaked across sessions",
			got, firstSaw)
	}
}

// messagesSeen pulls the count out of the mock driver's reply.
func messagesSeen(t *testing.T, reply string) int {
	t.Helper()
	_, rest, ok := strings.Cut(reply, "(saw ")
	if !ok {
		t.Fatalf("reply %q does not carry a message count; the mock driver changed shape", reply)
	}
	var n int
	if _, err := fmt.Sscanf(rest, "%d messages)", &n); err != nil {
		t.Fatalf("could not read the message count from %q: %v", reply, err)
	}
	return n
}

// A node configured with a driver that does not exist must fail at
// boot with a message naming what is available. The alternative —
// booting and failing on the first turn — moves a typo from startup
// into production.
func TestUnknownDriverFailsAtBoot(t *testing.T) {
	t.Parallel()
	cfg := mockNodeConfig(t)
	cfg.Compute.Providers[0].Driver = "anthropc" // typo

	_, err := node.New(cfg)
	if err == nil {
		t.Fatal("a node booted with an unknown driver; the failure would surface on the first turn instead")
	}
	if !contains(err.Error(), "anthropc") || !contains(err.Error(), "available") {
		t.Errorf("error should name the bad driver and what is available, got: %v", err)
	}
}

// --- harness ---------------------------------------------------------

type mockNode struct {
	n    *node.Node
	addr string
}

func mockNodeConfig(t *testing.T) node.Config {
	t.Helper()
	tmp := t.TempDir()
	nodeID := "e2e-turn"

	memKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	storageRoot := filepath.Join(tmp, "store")
	if err := os.MkdirAll(storageRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	return node.Config{
		NodeID:     nodeID,
		Functions:  []types.NodeFunction{types.FunctionMemory, types.FunctionCompute, types.FunctionGateway, types.FunctionStorage},
		ListenAddr: "127.0.0.1:0",
		Creds:      mustSignNodeCertForIntegration(t, filepath.Join(tmp, "certs"), nodeID),
		MemoryKey:  memKey,
		DataDir:    filepath.Join(tmp, "data"),
		Bootstrap:  true,
		// Required for a memory node with no seeds — a single-node
		// cluster with no off-cluster backup is rejected at config
		// validation, which is a good rule and not one to bypass here.
		SnapshotTarget: "storage:store",
		Compute: config.ComputeConfig{
			// The whole provider set is mock. No network, and the
			// config path is what selects it.
			Providers: []config.ProviderConfig{{
				Label:  "main",
				Driver: compute.DriverMock,
				Model:  "mock-main",
				// A mock touches no network, so it is local by
				// definition — and the trust floor is enforced on every
				// provider regardless of driver, which is correct.
				TrustTier: types.TrustLocal,
			}},
		},
		Gateway: config.GatewayConfig{
			Enabled:          true,
			HTTPPort:         0,
			UnknownUserScope: "public",
		},
		Storage: config.StorageConfig{
			Enabled: true,
			Mounts: []config.StorageMountConfig{
				{Label: "store", Type: "local", Path: storageRoot, Mode: "rw"},
			},
		},
	}
}

func bootMockNode(t *testing.T) *mockNode {
	t.Helper()
	n, err := node.New(mockNodeConfig(t))
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("node.Start did not return within 5s of cancel")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for n.Gateway() == nil || n.Gateway().Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("gateway did not bind within 10s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &mockNode{n: n, addr: n.Gateway().Addr()}
}

type messageReply struct {
	Reply string `json:"reply"`
}

func (h *mockNode) send(t *testing.T, text, session string) messageReply {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"message":    text,
		"session_id": session,
	})
	resp, err := http.Post("http://"+h.addr+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/messages: status %d: %s", resp.StatusCode, raw)
	}
	var out messageReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode reply: %v (body: %s)", err, raw)
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && bytes.Contains([]byte(haystack), []byte(needle))
}
