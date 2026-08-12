package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// startRESTWithSessions is startREST plus a durable session store, so
// a test can assert what survives across server instances.
func startRESTWithSessions(t *testing.T, agent *compute.Agent, sessions SessionStore) string {
	t.Helper()
	srv := NewServer(RESTConfig{Addr: "127.0.0.1:0", Sessions: sessions}, agent)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Start(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Addr() == "" {
		cancel()
		wg.Wait()
		t.Fatal("server didn't bind within 1s")
	}
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return "http://" + srv.Addr()
}

func postMessage(t *testing.T, url string, body messageRequest) messageResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"/v1/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/messages: status %d", resp.StatusCode)
	}
	var out messageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A second request carrying the same session_id must arrive at the
// model with the first exchange already in the message list.
func TestRESTSessionIDCarriesHistoryIntoNextTurn(t *testing.T) {
	t.Parallel()
	provider := compute.NewMockProvider(
		compute.MockResponse{Content: "first reply"},
		compute.MockResponse{Content: "second reply"},
	)
	agent, err := compute.NewAgent(compute.AgentConfig{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	url := startRESTWithSessions(t, agent, newFakeSessionStore())

	postMessage(t, url, messageRequest{Message: "my name is james", TurnID: "t1", SessionID: "s1"})
	postMessage(t, url, messageRequest{Message: "what is my name?", TurnID: "t2", SessionID: "s1"})

	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider saw %d calls, want 2", len(calls))
	}
	var got []string
	for _, m := range calls[1].Messages {
		got = append(got, m.Role+":"+m.Content)
	}
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "my name is james") {
		t.Errorf("second turn lost the first user message; messages were: %s", joined)
	}
	if !strings.Contains(joined, "first reply") {
		t.Errorf("second turn lost the first assistant reply; messages were: %s", joined)
	}
}

// Without a session_id the endpoint stays stateless — a shared token
// firing independent one-shot requests must not accumulate a thread.
func TestRESTWithoutSessionIDStaysStateless(t *testing.T) {
	t.Parallel()
	provider := compute.NewMockProvider(
		compute.MockResponse{Content: "first reply"},
		compute.MockResponse{Content: "second reply"},
	)
	agent, err := compute.NewAgent(compute.AgentConfig{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	url := startRESTWithSessions(t, agent, newFakeSessionStore())

	postMessage(t, url, messageRequest{Message: "one", TurnID: "t1"})
	postMessage(t, url, messageRequest{Message: "two", TurnID: "t2"})

	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider saw %d calls, want 2", len(calls))
	}
	for _, m := range calls[1].Messages {
		if strings.Contains(m.Content, "one") {
			t.Errorf("stateless request leaked prior turn: %+v", calls[1].Messages)
		}
	}
}

// The point of the durable store: a brand-new server process picks up
// the conversation where the previous one left off.
func TestRESTSessionSurvivesServerRestart(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()

	first, err := compute.NewAgent(compute.AgentConfig{
		Provider: compute.NewMockProvider(compute.MockResponse{Content: "nice to meet you"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	url := startRESTWithSessions(t, first, store)
	postMessage(t, url, messageRequest{Message: "my name is james", TurnID: "t1", SessionID: "s1"})

	// New server, new agent, new in-memory cache — only the store
	// carries over, exactly as it would across a process restart.
	provider := compute.NewMockProvider(compute.MockResponse{Content: "you are james"})
	second, err := compute.NewAgent(compute.AgentConfig{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	url2 := startRESTWithSessions(t, second, store)
	postMessage(t, url2, messageRequest{Message: "what is my name?", TurnID: "t2", SessionID: "s1"})

	calls := provider.Calls()
	if len(calls) != 1 {
		t.Fatalf("second server's provider saw %d calls, want 1", len(calls))
	}
	var joined string
	for _, m := range calls[0].Messages {
		joined += m.Role + ":" + m.Content + " | "
	}
	if !strings.Contains(joined, "my name is james") {
		t.Errorf("conversation did not survive restart; messages were: %s", joined)
	}
}

func TestRESTSessionsAreIsolatedFromEachOther(t *testing.T) {
	t.Parallel()
	provider := compute.NewMockProvider(
		compute.MockResponse{Content: "reply a"},
		compute.MockResponse{Content: "reply b"},
	)
	agent, err := compute.NewAgent(compute.AgentConfig{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	url := startRESTWithSessions(t, agent, newFakeSessionStore())

	postMessage(t, url, messageRequest{Message: "alice speaking", TurnID: "t1", SessionID: "alice"})
	postMessage(t, url, messageRequest{Message: "bob speaking", TurnID: "t2", SessionID: "bob"})

	calls := provider.Calls()
	for _, m := range calls[1].Messages {
		if strings.Contains(m.Content, "alice") {
			t.Errorf("bob's session saw alice's message: %+v", calls[1].Messages)
		}
	}
}

// A node with no memory function wired has a nil store; the endpoint
// must still work, just without persistence.
func TestRESTSessionIDWithoutStoreDoesNotFail(t *testing.T) {
	t.Parallel()
	agent, err := compute.NewAgent(compute.AgentConfig{
		Provider: compute.NewMockProvider(compute.MockResponse{Content: "ok"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	url := startRESTWithSessions(t, agent, nil)
	got := postMessage(t, url, messageRequest{Message: "hello", TurnID: "t1", SessionID: "s1"})
	if got.Reply != "ok" {
		t.Errorf("reply = %q, want ok", got.Reply)
	}
}
