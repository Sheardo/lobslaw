package anthropic

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

// Anthropic's vision wire shape.
//
// These assertions moved here from internal/compute when read_image
// stopped switching on a format enum. They are the proof the move
// preserved behaviour, which is the only thing that makes a refactor
// of this size safe — so they check the BYTES on the wire, not that a
// function was called.

func TestVisionSendsAnthropicsImageShape(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"a tiny png"}]}`))
	}))
	t.Cleanup(srv.Close)

	d, err := VisionFactory(compute.VisionDriverConfig{
		Endpoint:   srv.URL,
		Model:      "claude-opus-4",
		Credential: compute.NewHeaderCredential("x-api-key", "sk-ant-x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Describe(context.Background(), compute.VisionRequest{
		Question: "what is this?",
		MIME:     "image/png",
		Data:     []byte{0x89, 'P', 'N', 'G'},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a tiny png" {
		t.Errorf("content = %q", got)
	}
	if !strings.Contains(string(gotBody), `"source"`) ||
		!strings.Contains(string(gotBody), `"media_type":"image/png"`) {
		t.Errorf("not Anthropic's image source shape: %s", gotBody)
	}
	if gotKey != "sk-ant-x" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	// The protocol version is the DRIVER's business, not the
	// operator's. A driver that left it to the wiring layer would work
	// only for whoever remembered to set it.
	if gotVersion != apiVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, apiVersion)
	}
}

// A reply may arrive as several text parts, and taking only the first
// would silently truncate a long description at whatever boundary the
// provider happened to choose.
func TestVisionConcatenatesEveryTextPart(t *testing.T) {
	t.Parallel()
	got, err := decodeVision([]byte(
		`{"content":[{"type":"text","text":"one "},{"type":"thinking","text":"skip"},{"type":"text","text":"two"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "one two" {
		t.Errorf("got %q, want the text parts joined and the rest ignored", got)
	}
}

// An HTTP failure must be classified, or the failover chain reads every
// failure as permanent and never advances to the backup.
func TestVisionClassifiesAnHTTPFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	d, err := VisionFactory(compute.VisionDriverConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Describe(context.Background(), compute.VisionRequest{MIME: "image/png"})
	if err == nil {
		t.Fatal("a 503 was not an error")
	}
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
