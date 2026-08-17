package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// Gemini's generateContent wire shape.
//
// Moved here from internal/compute when read_image stopped switching
// on a format enum, and kept as byte-level assertions because that is
// what proves the move preserved behaviour.

func TestVisionSendsGeminisInlineDataShape(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotKeyParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotKeyParam = r.URL.Query().Get("key")
		_, _ = w.Write([]byte(
			`{"candidates":[{"content":{"parts":[{"text":"a jpeg header"}]}}]}`))
	}))
	t.Cleanup(srv.Close)

	d, err := VisionFactory(compute.VisionDriverConfig{
		Endpoint: srv.URL,
		Model:    "gemini-2.0-flash",
		// Google authenticates with a query parameter, not a header.
		// That used to be done by appending "?key=" to the endpoint
		// before the request was built, which put one provider's auth
		// somewhere no other provider's was.
		Credential: compute.NewQueryCredential("key", "g-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Describe(context.Background(), compute.VisionRequest{
		Question: "what is this?",
		MIME:     "image/jpeg",
		Data:     []byte{0xFF, 0xD8, 0xFF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a jpeg header" {
		t.Errorf("content = %q", got)
	}
	if gotKeyParam != "g-key" {
		t.Errorf("key query param = %q; Gemini authenticates on the URL", gotKeyParam)
	}
	if !strings.Contains(string(gotBody), `"inlineData"`) ||
		!strings.Contains(string(gotBody), `"mimeType":"image/jpeg"`) {
		t.Errorf("not Gemini's inlineData shape: %s", gotBody)
	}
}

// An endpoint that already carries a key must not end up with two.
func TestAQueryCredentialReplacesAnExistingKey(t *testing.T) {
	t.Parallel()
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	t.Cleanup(srv.Close)

	d, err := VisionFactory(compute.VisionDriverConfig{
		Endpoint:   srv.URL + "?key=stale&alt=json",
		Model:      "gemini-2.0-flash",
		Credential: compute.NewQueryCredential("key", "fresh"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Describe(context.Background(), compute.VisionRequest{MIME: "image/png"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(gotRawQuery, "key=") != 1 || !strings.Contains(gotRawQuery, "key=fresh") {
		t.Errorf("query = %q; want exactly one key, the fresh one", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "alt=json") {
		t.Errorf("query = %q; an unrelated parameter was dropped", gotRawQuery)
	}
}

// Every text part of every candidate, for the same reason as the
// Anthropic driver: taking the first would truncate silently.
func TestVisionConcatenatesEveryPart(t *testing.T) {
	t.Parallel()
	got, err := decodeVision([]byte(
		`{"candidates":[{"content":{"parts":[{"text":"one "},{"text":"two"}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "one two" {
		t.Errorf("got %q", got)
	}
}

func TestVisionClassifiesAnHTTPFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	d, err := VisionFactory(compute.VisionDriverConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Describe(context.Background(), compute.VisionRequest{MIME: "image/png"})
	var de *compute.DriverError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v (%T); it carries no failure class", err, err)
	}
}

func TestVisionNeedsAnEndpointAndModel(t *testing.T) {
	t.Parallel()
	if _, err := VisionFactory(compute.VisionDriverConfig{Model: "m"}); err == nil {
		t.Error("no endpoint was accepted")
	}
	if _, err := VisionFactory(compute.VisionDriverConfig{Endpoint: "http://x"}); err == nil {
		t.Error("no model was accepted")
	}
}
