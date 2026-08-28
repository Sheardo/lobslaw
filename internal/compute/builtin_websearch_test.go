package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubSearchDriver answers from a fixed script, recording that it ran.
type stubSearchDriver struct {
	name    string
	results []SearchResult
	err     error
	calls   *[]string
	lastReq SearchRequest
}

func (d *stubSearchDriver) Search(_ context.Context, req SearchRequest) ([]SearchResult, error) {
	if d.calls != nil {
		*d.calls = append(*d.calls, d.name)
	}
	d.lastReq = req
	if d.err != nil {
		return nil, d.err
	}
	return d.results, nil
}

func TestRegisterWebSearchBuiltinRequiresProvider(t *testing.T) {
	t.Parallel()
	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b); err == nil {
		t.Error("no provider config should fail register")
	}
	if err := RegisterWebSearchBuiltin(b, WebSearchConfig{Label: "x"}); err == nil {
		t.Error("a config with no driver should fail register")
	}
}

// The output envelope is a contract, not an implementation detail: the
// prompt tells the model to cite [title](url) from it, and every
// transcript ever written contains it. A driver swap must not be
// visible in this JSON.
func TestWebSearchEnvelopeIsBackendAgnostic(t *testing.T) {
	t.Parallel()
	driver := &stubSearchDriver{results: []SearchResult{
		{Title: "Go Generics Explained", URL: "https://go.dev/x", Text: strings.Repeat("a", 1000)},
		{Title: "Generics FAQ", URL: "https://go.dev/faq", Text: "short"},
	}}
	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b, WebSearchConfig{Label: "stub", Driver: driver}); err != nil {
		t.Fatal(err)
	}
	fn, ok := b.Get("web_search")
	if !ok {
		t.Fatal("web_search not registered")
	}
	stdout, exit, err := fn(context.Background(), map[string]string{
		"query":       "golang generics",
		"num_results": "3",
	})
	if err != nil || exit != 0 {
		t.Fatalf("search: exit=%d err=%v", exit, err)
	}
	if driver.lastReq.Query != "golang generics" || driver.lastReq.NumResults != 3 {
		t.Errorf("driver saw %+v", driver.lastReq)
	}
	if driver.lastReq.Depth != "auto" {
		t.Errorf("depth = %q; want the schema default", driver.lastReq.Depth)
	}

	var payload struct {
		Query   string         `json:"query"`
		Results []SearchResult `json:"results"`
	}
	if err := json.Unmarshal(stdout, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Query != "golang generics" {
		t.Errorf("echoed query = %q", payload.Query)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results = %d; want 2", len(payload.Results))
	}
	if !strings.HasSuffix(payload.Results[0].Text, "…") {
		t.Errorf("long snippet should be truncated with …; got len=%d", len(payload.Results[0].Text))
	}
	// engine is omitempty, so a backend that doesn't report one adds
	// no field the model has to reason about.
	if strings.Contains(string(stdout), "engine") {
		t.Errorf("empty engine should not appear in the envelope: %s", stdout)
	}
}

func TestWebSearchClampsNumResults(t *testing.T) {
	t.Parallel()
	driver := &stubSearchDriver{}
	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b, WebSearchConfig{Driver: driver}); err != nil {
		t.Fatal(err)
	}
	fn, _ := b.Get("web_search")
	for _, raw := range []string{"0", "-1", "99", "banana"} {
		if _, _, err := fn(context.Background(), map[string]string{"query": "q", "num_results": raw}); err != nil {
			t.Fatalf("num_results=%q: %v", raw, err)
		}
		if driver.lastReq.NumResults != DefaultSearchResults {
			t.Errorf("num_results=%q gave %d; want the default %d",
				raw, driver.lastReq.NumResults, DefaultSearchResults)
		}
	}
}

func TestWebSearchBuiltinRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b, WebSearchConfig{Driver: &stubSearchDriver{}}); err != nil {
		t.Fatal(err)
	}
	fn, _ := b.Get("web_search")
	_, exit, err := fn(context.Background(), map[string]string{})
	if err == nil || exit == 0 {
		t.Error("empty query should fail")
	}
}

// The reason web_search became variadic: a self-hosted SearXNG having
// a bad minute should fall through to whatever else is configured,
// rather than leaving the agent with no way to look anything up.
func TestWebSearchFallsOverToTheNextBackend(t *testing.T) {
	t.Parallel()
	calls := &[]string{}
	primary := &stubSearchDriver{name: "searxng", err: Transient(errors.New("connection refused")), calls: calls}
	backup := &stubSearchDriver{name: "exa", calls: calls,
		results: []SearchResult{{Title: "T", URL: "https://example.com"}}}

	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b,
		WebSearchConfig{Label: "searxng", Driver: primary},
		WebSearchConfig{Label: "exa", Driver: backup},
	); err != nil {
		t.Fatal(err)
	}
	fn, _ := b.Get("web_search")
	stdout, exit, err := fn(context.Background(), map[string]string{"query": "q"})
	if err != nil || exit != 0 {
		t.Fatalf("chain should have recovered: exit=%d err=%v", exit, err)
	}
	if got := strings.Join(*calls, ","); got != "searxng,exa" {
		t.Errorf("call order = %q; want searxng,exa", got)
	}
	if !strings.Contains(string(stdout), "https://example.com") {
		t.Errorf("backup's results should be returned; got %s", stdout)
	}
}

// A permanent failure must NOT walk the chain: every backend would
// reject the same thing and the operator would read the last one's
// error instead of the first one's.
func TestWebSearchDoesNotFallOverOnPermanent(t *testing.T) {
	t.Parallel()
	calls := &[]string{}
	primary := &stubSearchDriver{name: "searxng", calls: calls,
		err: Permanent(errors.New("searxng search: JSON API disabled"))}
	backup := &stubSearchDriver{name: "exa", calls: calls}

	b := NewBuiltins()
	if err := RegisterWebSearchBuiltin(b,
		WebSearchConfig{Label: "searxng", Driver: primary},
		WebSearchConfig{Label: "exa", Driver: backup},
	); err != nil {
		t.Fatal(err)
	}
	fn, _ := b.Get("web_search")
	if _, _, err := fn(context.Background(), map[string]string{"query": "q"}); err == nil {
		t.Fatal("permanent failure should surface")
	}
	if got := strings.Join(*calls, ","); got != "searxng" {
		t.Errorf("call order = %q; want searxng alone", got)
	}
}

// TestServerToolsMergedIntoRequest — server tools supplied to
// LLMClientConfig appear in the wire-shape tools array alongside
// function tools.
func TestServerToolsMergedIntoRequest(t *testing.T) {
	t.Parallel()
	var captured openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	client, err := NewLLMClient(LLMClientConfig{
		Endpoint: srv.URL,
		Model:    "test",
		ServerTools: []ServerTool{
			{Type: "openrouter:web_search", Parameters: map[string]any{"max_results": 5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Tools) != 1 {
		t.Fatalf("tools = %d; want 1", len(captured.Tools))
	}
	entry, ok := captured.Tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool entry not an object: %T", captured.Tools[0])
	}
	if entry["type"] != "openrouter:web_search" {
		t.Errorf("type = %v", entry["type"])
	}
}
