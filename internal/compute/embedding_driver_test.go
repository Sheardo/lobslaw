package compute

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embedding path had five switch sites on an EmbeddingFormat and
// no tests at all. These cover the drivers that replaced them, and the
// client behaviour around them.

func embedServer(t *testing.T, reply string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var body []byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &auth
}

// --- the OpenAI shape --------------------------------------------------

// `input` is a bare STRING on the single path and an ARRAY on the
// batch path. That difference is the whole reason the driver interface
// has both methods; collapsing them would change the bytes every
// existing deployment sends.
func TestOpenAIEmbeddingSendsAStringForOneAndAnArrayForMany(t *testing.T) {
	t.Parallel()
	srv, body, _ := embedServer(t,
		`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`)
	d, err := OpenAIEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.Embed(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), `"input":"one"`) {
		t.Errorf("single embed sent %s; want a bare string", *body)
	}

	if _, err := d.EmbedBatch(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), `"input":["a","b"]`) {
		t.Errorf("batch embed sent %s; want an array", *body)
	}
}

// THE SUBTLE ONE. The API does not promise the array comes back
// sorted, and a batch reassembled in arrival order attaches every
// memory to the wrong text — which nothing downstream can detect,
// because a vector is a plausible vector whichever text it came from.
func TestOpenAIEmbeddingPlacesVectorsByIndexNotArrivalOrder(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t,
		`{"data":[{"index":2,"embedding":[3]},{"index":0,"embedding":[1]},{"index":1,"embedding":[2]}]}`)
	d, err := OpenAIEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float32{1, 2, 3} {
		if len(got[i]) != 1 || got[i][0] != want {
			t.Errorf("slot %d = %v, want [%v] — vectors were placed in arrival order", i, got[i], want)
		}
	}
}

// An index outside the batch would write past the slice or silently
// overwrite a neighbour's vector.
func TestOpenAIEmbeddingRefusesAnOutOfRangeIndex(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, `{"data":[{"index":7,"embedding":[1]}]}`)
	d, _ := OpenAIEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if _, err := d.EmbedBatch(context.Background(), []string{"a"}); err == nil {
		t.Error("an out-of-range index was accepted")
	}
}

func TestOpenAIEmbeddingRefusesAShortBatch(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, `{"data":[{"index":0,"embedding":[1]}]}`)
	d, _ := OpenAIEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if _, err := d.EmbedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("two inputs and one vector was accepted")
	}
}

// --- the MiniMax shape -------------------------------------------------

// MiniMax reports failure in base_resp on an HTTP 200. Without this
// the caller gets zero vectors and no reason.
func TestMiniMaxEmbeddingSurfacesAnInBodyFailure(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t,
		`{"vectors":[],"base_resp":{"status_code":1004,"status_msg":"invalid api key"}}`)
	d, err := MiniMaxEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("an HTTP 200 carrying status_code 1004 was treated as success")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("err = %q; it does not carry the provider's reason", err)
	}
}

// MiniMax takes an array either way, so its single and batch paths are
// genuinely the same request — unlike OpenAI's.
func TestMiniMaxEmbeddingAlwaysSendsAnArray(t *testing.T) {
	t.Parallel()
	srv, body, _ := embedServer(t, `{"vectors":[[0.1]],"base_resp":{"status_code":0}}`)
	d, _ := MiniMaxEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	if _, err := d.Embed(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), `"texts":["one"]`) {
		t.Errorf("sent %s; want a texts array", *body)
	}
}

// --- shared behaviour --------------------------------------------------

func TestBothEmbeddingDriversPresentTheirCredential(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]EmbeddingDriverFactory{
		"openai":  OpenAIEmbeddingFactory,
		"minimax": MiniMaxEmbeddingFactory,
	} {
		srv, _, auth := embedServer(t,
			`{"data":[{"index":0,"embedding":[1]}],"vectors":[[1]],"base_resp":{"status_code":0}}`)
		d, err := f(EmbeddingDriverConfig{
			Endpoint: srv.URL, Model: "m",
			Credential: NewBearerCredential("sk-test"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Embed(context.Background(), "x"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if *auth != "Bearer sk-test" {
			t.Errorf("%s: Authorization = %q; the credential was dropped", name, *auth)
		}
	}
}

// An HTTP failure must be classified, or a chain that could have
// advanced reads it as permanent.
func TestEmbeddingHTTPFailuresAreClassified(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	d, _ := OpenAIEmbeddingFactory(EmbeddingDriverConfig{Endpoint: srv.URL, Model: "m"})
	_, err := d.Embed(context.Background(), "x")
	var de *DriverError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v (%T); it carries no failure class", err, err)
	}
}

func TestEmbeddingFactoriesNeedAnEndpointAndModel(t *testing.T) {
	t.Parallel()
	for name, f := range map[string]EmbeddingDriverFactory{
		"openai":  OpenAIEmbeddingFactory,
		"minimax": MiniMaxEmbeddingFactory,
	} {
		if _, err := f(EmbeddingDriverConfig{Model: "m"}); err == nil {
			t.Errorf("%s: no endpoint was accepted", name)
		}
		if _, err := f(EmbeddingDriverConfig{Endpoint: "http://x"}); err == nil {
			t.Errorf("%s: no model was accepted", name)
		}
	}
}

// The mock embeds the same text the same way twice, or a similarity
// test built on it means nothing.
func TestTheMockEmbeddingDriverIsDeterministic(t *testing.T) {
	t.Parallel()
	d, err := MockEmbeddingFactory(EmbeddingDriverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := d.Embed(context.Background(), "hello")
	b, _ := d.Embed(context.Background(), "hello")
	c, _ := d.Embed(context.Background(), "different")
	if len(a) == 0 {
		t.Fatal("empty vector")
	}
	if !equalVec(a, b) {
		t.Error("the same text embedded two different ways")
	}
	if equalVec(a, c) {
		t.Error("different texts embedded identically; a similarity test on this proves nothing")
	}
}

func equalVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- the client's own job ----------------------------------------------

// The client filters empty inputs and re-projects the rest into the
// caller's original slots. Getting that wrong shifts every vector one
// place along, which is the same silent mis-attachment as the index
// bug above.
func TestTheClientReprojectsAroundEmptyInputs(t *testing.T) {
	t.Parallel()
	srv, body, _ := embedServer(t,
		`{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2]}]}`)
	c, err := NewEmbeddingClient(EmbeddingClientConfig{
		Endpoint: srv.URL, Model: "m", Dims: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.EmbedBatch(context.Background(), []string{"a", "   ", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d slots for 3 inputs", len(got))
	}
	if got[1] != nil {
		t.Errorf("slot 1 was empty input; got %v", got[1])
	}
	if len(got[0]) != 1 || got[0][0] != 1 || len(got[2]) != 1 || got[2][0] != 2 {
		t.Errorf("vectors landed in the wrong slots: %v", got)
	}
	// The blank must not have been sent.
	var sent struct {
		Input []string `json:"input"`
	}
	_ = json.Unmarshal(*body, &sent)
	if len(sent.Input) != 2 {
		t.Errorf("sent %v; the empty input should have been filtered", sent.Input)
	}
}

// A model whose output width does not match the declared dims would
// corrupt the index silently, so the mismatch is refused.
func TestTheClientChecksTheDeclaredDimension(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, `{"data":[{"index":0,"embedding":[1,2,3]}]}`)
	c, err := NewEmbeddingClient(EmbeddingClientConfig{
		Endpoint: srv.URL, Model: "m", Dims: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Error("a 3-dim vector was accepted for a 768-dim config")
	}
}

// The suffix rule lives in the client, which is why the driver is
// built from a factory rather than handed in ready-made.
func TestTheClientAppendsTheEmbeddingsSuffixOnce(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
	}))
	t.Cleanup(srv.Close)

	for _, base := range []string{srv.URL + "/v1", srv.URL + "/v1/", srv.URL + "/v1/embeddings"} {
		c, err := NewEmbeddingClient(EmbeddingClientConfig{Endpoint: base, Model: "m", Dims: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Embed(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
		if gotPath != "/v1/embeddings" {
			t.Errorf("endpoint %q produced path %q", base, gotPath)
		}
	}
}

// No key means no header. A self-hosted embedding server usually wants
// no auth at all, and "Bearer " with an empty value is worse than
// nothing.
func TestNoAPIKeyMeansNoAuthorizationHeader(t *testing.T) {
	t.Parallel()
	srv, _, auth := embedServer(t, `{"data":[{"index":0,"embedding":[1]}]}`)
	c, err := NewEmbeddingClient(EmbeddingClientConfig{Endpoint: srv.URL, Model: "m", Dims: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if *auth != "" {
		t.Errorf("Authorization = %q with no API key configured", *auth)
	}
}
