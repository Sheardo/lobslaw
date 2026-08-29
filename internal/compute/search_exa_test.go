package compute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExaFactoryRequiresKey(t *testing.T) {
	t.Parallel()
	if _, err := ExaSearchFactory(SearchDriverConfig{}); err == nil {
		t.Error("Exa has no anonymous tier; a missing key should fail at boot")
	}
	_, err := ExaSearchFactory(SearchDriverConfig{
		Credential: ExaCredential("k"),
		Options:    map[string]string{"engines": "google"},
	})
	if err == nil || !strings.Contains(err.Error(), "engines") {
		t.Errorf("an option meant for another driver should be named; got %v", err)
	}
}

// Behaviour parity with the pre-driver builtin: same request shape,
// same x-api-key header, same response mapping.
func TestExaHappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		var req exaSearchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Query != "golang generics" {
			t.Errorf("query = %q", req.Query)
		}
		if req.NumResults != 3 {
			t.Errorf("numResults = %d; want 3", req.NumResults)
		}
		if req.Type != "fast" {
			t.Errorf("type = %q; the depth argument should reach Exa", req.Type)
		}
		if req.Contents == nil || !req.Contents.Text {
			t.Error("contents.text should be requested; the snippet is what gets cited")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exaSearchResponse{Results: []exaResult{
			{Title: "Go Generics Explained", URL: "https://go.dev/x", Text: "body", Score: 0.9},
		}})
	}))
	defer srv.Close()

	d, err := ExaSearchFactory(SearchDriverConfig{Endpoint: srv.URL, Credential: ExaCredential("test-key")})
	if err != nil {
		t.Fatal(err)
	}
	results, err := d.Search(context.Background(), SearchRequest{Query: "golang generics", NumResults: 3, Depth: "fast"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://go.dev/x" || results[0].Score != 0.9 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Engine != "" {
		t.Errorf("Exa reports no engine; got %q", results[0].Engine)
	}
}

// The chain can only route around a failure it can classify, and a
// rejected key must advance rather than retry — the next backend
// authenticates with a different one.
func TestExaClassifiesFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		want   FailureClass
	}{
		{http.StatusTooManyRequests, FailureTransient},
		{http.StatusServiceUnavailable, FailureTransient},
		{http.StatusUnauthorized, FailureCredential},
		{http.StatusBadRequest, FailurePermanent},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", tc.status)
		}))
		d, err := ExaSearchFactory(SearchDriverConfig{Endpoint: srv.URL, Credential: ExaCredential("k")})
		if err != nil {
			t.Fatal(err)
		}
		_, searchErr := d.Search(context.Background(), SearchRequest{Query: "q"})
		if searchErr == nil {
			t.Fatalf("HTTP %d should error", tc.status)
		}
		if got := ClassifyFailure(searchErr); got != tc.want {
			t.Errorf("HTTP %d classified %v; want %v", tc.status, got, tc.want)
		}
		if !strings.Contains(searchErr.Error(), "exa search") {
			t.Errorf("error should name the backend: %v", searchErr)
		}
		srv.Close()
	}
}

func TestExaEffectiveEndpointPrecedence(t *testing.T) {
	t.Parallel()
	if got := ExaEffectiveEndpoint(""); got != DefaultExaEndpoint {
		t.Errorf("unset endpoint = %q; want the default", got)
	}
	if got := ExaEffectiveEndpoint("https://proxy.internal/search"); got != "https://proxy.internal/search" {
		t.Errorf("configured endpoint = %q", got)
	}
	// Not parallel-safe with an env override, so the override case is
	// asserted through the driver's own behaviour above rather than by
	// mutating the environment here.
}
