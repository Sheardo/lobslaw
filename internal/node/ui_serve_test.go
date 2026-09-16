package node_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/node"
	"github.com/jmylchreest/lobslaw/pkg/config"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A booted node with [gateway.ui] enabled actually serves the console,
// and the API it talks to is on the same listener.
//
// The unit tests cover the handler and the routes separately; this is
// the one that would catch them being wired to different muxes, served
// on different ports, or the console shadowing /v1 — none of which any
// of those tests can see.
func TestNodeServesTheWebConsoleAndAPIOnOneListener(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping console boot integration in short mode")
	}

	tmp := t.TempDir()
	nodeID := "ui-boot-node"
	n, err := node.New(node.Config{
		NodeID:         nodeID,
		Functions:      []types.NodeFunction{types.FunctionMemory, types.FunctionStorage, types.FunctionCompute},
		ListenAddr:     "127.0.0.1:0",
		DataDir:        filepath.Join(tmp, "data"),
		Bootstrap:      true,
		SnapshotTarget: "storage:test-backup",
		Creds:          signNodeCert(t, filepath.Join(tmp, "certs"), nodeID),
		MemoryKey:      mustKey(t),
		LLMProvider:    compute.NewMockProvider(compute.MockResponse{Content: "hello"}),
		Gateway: config.GatewayConfig{
			Enabled:          true,
			HTTPPort:         0,
			BindAddress:      "127.0.0.1",
			UnknownUserScope: "owner",
			UI:               config.UIConfig{Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("node.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()

	base := waitForGatewayAddr(t, n)
	waitForChief(ctx, t, n)

	t.Run("console is served at the root", func(t *testing.T) {
		body, status := get(t, base+"/")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if !strings.Contains(body, `<div id="root">`) {
			t.Errorf("the root did not serve the app shell:\n%s", truncateForLog(body))
		}
	})

	t.Run("a deep link reloads", func(t *testing.T) {
		_, status := get(t, base+"/bots/chief")
		if status != http.StatusOK {
			t.Errorf("status = %d; reloading a bot page 404s", status)
		}
	})

	t.Run("the API is on the same listener", func(t *testing.T) {
		body, status := get(t, base+"/v1/bots")
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, truncateForLog(body))
		}
		// If the console had shadowed the API route this would be the
		// app shell with a 200 on it, which a client then fails to
		// parse as JSON.
		if !strings.Contains(body, `"bots"`) {
			t.Errorf("/v1/bots did not return JSON:\n%s", truncateForLog(body))
		}
		if !strings.Contains(body, `"chief"`) {
			t.Errorf("the seeded chief is missing from the API:\n%s", truncateForLog(body))
		}
	})

	t.Run("an unknown API path 404s rather than returning HTML", func(t *testing.T) {
		body, status := get(t, base+"/v1/nope")
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", status)
		}
		if strings.Contains(body, `<div id="root">`) {
			t.Error("an API typo returned the app shell")
		}
	})

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned: %v", err)
	}
}

func waitForGatewayAddr(t *testing.T, n *node.Node) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if gw := n.Gateway(); gw != nil {
			if addr := gw.Addr(); addr != "" {
				return "http://" + addr
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the gateway never bound a listener")
	return ""
}

func get(t *testing.T, url string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body), res.StatusCode
}

func truncateForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
