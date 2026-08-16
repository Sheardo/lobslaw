package imagen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

type fake struct {
	srv    *httptest.Server
	path   string
	body   []byte
	status int
	json   string
}

func newFake(t *testing.T, status int, body string) *fake {
	t.Helper()
	f := &fake{status: status, json: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		f.body = buf
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.json))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newDriver(t *testing.T, f *fake, size string) *Driver {
	t.Helper()
	d, err := New(Config{
		Endpoint:   f.srv.URL + "/v1/projects/p/locations/l/publishers/google/models/imagen-4.0",
		Credential: compute.NewBearerCredential("tok"),
		Size:       size,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func okBody(b64 string) string {
	return `{"predictions":[{"bytesBase64Encoded":"` + b64 + `","mimeType":"image/png"}]}`
}

// The point of a second image vendor: nothing about its shape matches
// the first. If these assertions would also pass against the OpenAI
// driver, this driver is not testing the interface.
func TestDisagreesWithTheOpenAIShape(t *testing.T) {
	t.Parallel()
	b64 := base64.StdEncoding.EncodeToString([]byte("PNG"))
	f := newFake(t, http.StatusOK, okBody(b64))
	d := newDriver(t, f, "")

	art, err := d.Generate(context.Background(), compute.ImageRequest{Prompt: "a red cube"})
	if err != nil {
		t.Fatal(err)
	}

	// The operation is a suffix on a model resource path.
	if !strings.HasSuffix(f.path, ":predict") {
		t.Errorf("path = %q, want a :predict suffix", f.path)
	}

	// The prompt is nested in instances, and knobs live in a sibling
	// object rather than beside it.
	var sent request
	if err := json.Unmarshal(f.body, &sent); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, f.body)
	}
	if len(sent.Instances) != 1 || sent.Instances[0].Prompt != "a red cube" {
		t.Errorf("prompt not nested in instances: %+v", sent)
	}
	if sent.Parameters.SampleCount != 1 {
		t.Errorf("sampleCount = %d, want 1 — more would bill for images nobody asked for",
			sent.Parameters.SampleCount)
	}

	if art.Kind != compute.ArtifactInline || string(art.Bytes) != "PNG" {
		t.Errorf("artifact = %+v, want decoded inline bytes", art)
	}
}

// A blocked prompt arrives as a 200 with a reason and no image, so
// status-code classification alone would call it success.
func TestSafetyBlockIsPermanentDespiteA200(t *testing.T) {
	t.Parallel()
	f := newFake(t, http.StatusOK,
		`{"predictions":[{"raiFilteredReason":"violates policy 4.2"}]}`)
	d := newDriver(t, f, "")

	_, err := d.Generate(context.Background(), compute.ImageRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a filtered prompt was reported as success")
	}
	if compute.ClassifyFailure(err) != compute.FailurePermanent {
		t.Errorf("classified %s, want permanent — the same prompt is refused everywhere",
			compute.ClassifyFailure(err))
	}
	if !strings.Contains(err.Error(), "violates policy 4.2") {
		t.Errorf("error should carry the vendor's reason: %v", err)
	}
}

// Sizes the tool exposes are WxH; this vendor takes ratios.
func TestSizeTranslatesToAspectRatio(t *testing.T) {
	t.Parallel()
	for size, want := range map[string]string{
		"1024x1024": "1:1",
		"1792x1008": "16:9",
		"1008x1792": "9:16",
		"16:9":      "16:9", // already a ratio, passed through
		"":          "",
		"weird":     "",
	} {
		b64 := base64.StdEncoding.EncodeToString([]byte("PNG"))
		f := newFake(t, http.StatusOK, okBody(b64))
		d := newDriver(t, f, "")
		if _, err := d.Generate(context.Background(), compute.ImageRequest{Prompt: "x", Size: size}); err != nil {
			t.Fatal(err)
		}
		var sent request
		_ = json.Unmarshal(f.body, &sent)
		if sent.Parameters.AspectRatio != want {
			t.Errorf("size %q → aspectRatio %q, want %q", size, sent.Parameters.AspectRatio, want)
		}
	}
}

func TestClassifiesFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		body   string
		want   compute.FailureClass
	}{
		{503, `{"error":{"message":"backend"}}`, compute.FailureTransient},
		{429, `{"error":{"message":"too many requests"}}`, compute.FailureTransient},
		// Vertex says "Quota exceeded" for per-minute RATE limits, not
		// a spent balance, so the shared marker list reads this as
		// quota-exhausted. Both classes advance the failover chain, so
		// the routing is right either way and only the operator-facing
		// wording is imprecise. Pinned here so the imprecision is
		// recorded rather than rediscovered.
		{429, `{"error":{"message":"Quota exceeded for online_prediction_requests"}}`,
			compute.FailureQuotaExhausted},
		{400, `{"error":{"message":"bad model"}}`, compute.FailurePermanent},
		{200, `{"predictions":[]}`, compute.FailureTransient},
	} {
		f := newFake(t, tc.status, tc.body)
		d := newDriver(t, f, "")
		_, err := d.Generate(context.Background(), compute.ImageRequest{Prompt: "x"})
		if err == nil {
			t.Fatalf("HTTP %d / %s produced no error", tc.status, tc.body)
		}
		if got := compute.ClassifyFailure(err); got != tc.want {
			t.Errorf("HTTP %d classified %s, want %s", tc.status, got, tc.want)
		}
	}
}

func TestRequiresCredentialAndEndpoint(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Endpoint: "https://x"}); err == nil {
		t.Error("built a driver with no credential")
	}
	if _, err := New(Config{Credential: compute.NewBearerCredential("t")}); err == nil {
		t.Error("built a driver with no endpoint; there is no sensible default resource path")
	}
}
