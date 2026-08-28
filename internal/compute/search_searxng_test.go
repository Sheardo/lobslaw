package compute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func searxngDriverFor(t *testing.T, endpoint string, opts map[string]string) SearchDriver {
	t.Helper()
	d, err := SearxngSearchFactory(SearchDriverConfig{Endpoint: endpoint, Options: opts})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return d
}

func TestSearxngFactoryRejectsBadConfig(t *testing.T) {
	t.Parallel()
	if _, err := SearxngSearchFactory(SearchDriverConfig{}); err == nil {
		t.Error("no endpoint should fail: a self-hosted instance has no default address")
	}
	_, err := SearxngSearchFactory(SearchDriverConfig{
		Endpoint: "http://searxng:8080/search",
		Options:  map[string]string{"time_rnage": "day"},
	})
	if err == nil || !strings.Contains(err.Error(), "time_rnage") {
		t.Errorf("a typo'd option should be named at boot; got %v", err)
	}
}

// Operators paste the address they use in a browser. Accepting the
// base URL and the full search path both is cheaper than the silent
// failure where the landing page comes back instead of results.
func TestSearxngAcceptsBaseOrSearchURL(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"http://searxng:8080", "http://searxng:8080/", "http://searxng:8080/search"} {
		got, err := searxngEndpoint(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != "http://searxng:8080/search" {
			t.Errorf("%q normalised to %q", in, got)
		}
	}
	// A path the operator did set is theirs to keep — a reverse proxy
	// mounting SearXNG under a prefix is a real deployment.
	got, _ := searxngEndpoint("https://s.example.com/searxng/search")
	if got != "https://s.example.com/searxng/search" {
		t.Errorf("explicit path rewritten to %q", got)
	}
}

func TestSearxngHappyPath(t *testing.T) {
	t.Parallel()
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"go generics","results":[
			{"url":"https://go.dev/x","title":"Generics","content":"a snippet","engine":"google","score":1.5,"publishedDate":null},
			{"url":"https://go.dev/faq","title":"FAQ","content":"another","engine":"duckduckgo","score":0.5}
		]}`))
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, map[string]string{
		"engines":  "google,duckduckgo",
		"language": "en",
		// Blank means "no preference", not "send an empty value".
		"categories": "",
	})
	results, err := d.Search(context.Background(), SearchRequest{Query: "go generics", NumResults: 5, Depth: "auto"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d; want 2", len(results))
	}
	if results[0].Title != "Generics" || results[0].URL != "https://go.dev/x" {
		t.Errorf("result[0] = %+v", results[0])
	}
	if results[0].Text != "a snippet" {
		t.Errorf("content should map to Text; got %q", results[0].Text)
	}
	if results[0].Engine != "google" {
		t.Errorf("engine = %q", results[0].Engine)
	}
	for _, want := range []string{"format=json", "q=go+generics", "engines=google%2Cduckduckgo", "language=en"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "categories") {
		t.Errorf("a blank option should not be sent: %q", gotQuery)
	}
}

// SearXNG has no result-count parameter, so the cap has to be applied
// on the way out or num_results silently means nothing.
func TestSearxngCapsResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"1"},{"title":"2"},{"title":"3"},{"title":"4"}]}`))
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, nil)
	results, err := d.Search(context.Background(), SearchRequest{Query: "q", NumResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("results = %d; want 2", len(results))
	}
}

// The single most common way a SearXNG integration fails: the JSON API
// is off by default and the instance answers with a 403 that explains
// nothing. Classifying that as a credential problem would send the
// operator hunting for a key SearXNG does not have.
func TestSearxngExplainsDisabledJSONAPI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"403 with html", http.StatusForbidden, "<html><body>Forbidden</body></html>"},
		{"200 with html", http.StatusOK, "<!DOCTYPE html><title>SearXNG</title>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			d := searxngDriverFor(t, srv.URL, nil)
			_, err := d.Search(context.Background(), SearchRequest{Query: "q"})
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "search.formats") && !strings.Contains(err.Error(), "formats:") {
				t.Errorf("error should name the settings.yml fix; got %v", err)
			}
			if got := ClassifyFailure(err); got != FailurePermanent {
				t.Errorf("class = %v; a misconfigured instance is not worth a retry or a credential hunt", got)
			}
		})
	}
}

func TestSearxngClassifiesUpstreamFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"upstream"}`, http.StatusBadGateway)
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, nil)
	_, err := d.Search(context.Background(), SearchRequest{Query: "q"})
	if got := ClassifyFailure(err); got != FailureTransient {
		t.Errorf("502 class = %v; want transient so the chain advances", got)
	}
}

// The first live test of this driver hit exactly this: SearXNG
// reachable, HTTP 200, and {"results":[]} because every upstream had
// CAPTCHA'd it. The agent got an empty list with exit 0 and concluded
// the backend was "misbehaving or unconfigured" — the diagnosis was in
// the response body the whole time.
func TestSearxngEmptyWithDeadEnginesIsAFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[["duckduckgo","CAPTCHA"],["google","Suspended: CAPTCHA"]]}`))
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, nil)
	_, err := d.Search(context.Background(), SearchRequest{Query: "q"})
	if err == nil {
		t.Fatal("every engine failing is a backend failure, not an empty answer")
	}
	for _, want := range []string{"duckduckgo (CAPTCHA)", "google (Suspended: CAPTCHA)", "engines"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
	// Transient so a chain fails over to another backend; CAPTCHAs and
	// rate limits pass, so it is also worth retrying later.
	if got := ClassifyFailure(err); got != FailureTransient {
		t.Errorf("class = %v; want transient", got)
	}
}

// A genuinely obscure query returns nothing and that is the answer.
// Erroring here would turn "no hits" into "backend broken".
func TestSearxngEmptyWithHealthyEnginesIsAnAnswer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[]}`))
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, nil)
	results, err := d.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("no hits is not an error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %+v", results)
	}
}

// Metasearch degrading to fewer engines is the normal case. Results
// present means success even when some upstreams are down.
func TestSearxngPartialEngineFailureStillSucceeds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://x","content":"c","engine":"google"}],
			"unresponsive_engines":[["duckduckgo","CAPTCHA"]]}`))
	}))
	defer srv.Close()

	d := searxngDriverFor(t, srv.URL, nil)
	results, err := d.Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("a partial result set is still a result set: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("results = %+v", results)
	}
}
