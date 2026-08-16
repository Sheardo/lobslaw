package veo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivertest"
)

type fake struct {
	srv        *httptest.Server
	lastPath   string
	lastBody   []byte
	submitJSON string
	pollJSON   string
	status     int
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		f.lastBody = buf
		w.WriteHeader(f.status)
		if strings.HasSuffix(r.URL.Path, ":predictLongRunning") {
			_, _ = w.Write([]byte(f.submitJSON))
			return
		}
		_, _ = w.Write([]byte(f.pollJSON))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newDriver(t *testing.T, f *fake, storageURI string) *Driver {
	t.Helper()
	d, err := New(Config{
		Endpoint:   f.srv.URL + "/v1/projects/p/locations/l/publishers/google/models/veo-3.0",
		Credential: compute.NewBearerCredential("tok"),
		StorageURI: storageURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const opName = "projects/p/locations/l/publishers/google/models/veo-3.0/operations/abc-123"

// The handle is a resource name with slashes, and polling POSTs it
// back in a body. Any design that treated a handle as an id to
// interpolate into a path breaks here — which is the point of adding
// this driver rather than a second DashScope-shaped one.
func TestHandleIsAResourceNamePostedBack(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"name":"` + opName + `","done":false}`
	d := newDriver(t, f, "")

	h, err := d.Submit(context.Background(), compute.JobRequest{Prompt: "a cube"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Raw != opName {
		t.Fatalf("handle raw = %q, want the whole resource name untouched", h.Raw)
	}
	if !strings.HasSuffix(f.lastPath, ":predictLongRunning") {
		t.Errorf("submit hit %q", f.lastPath)
	}

	// It has to survive storage, because that is how it comes back.
	enc, err := h.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := compute.DecodeJobHandle(enc)
	if err != nil {
		t.Fatalf("a resource name did not survive encoding: %v", err)
	}
	if back != h {
		t.Errorf("round trip changed the handle: %+v → %+v", h, back)
	}

	if _, err := d.Poll(context.Background(), back); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.lastPath, ":fetchPredictOperation") {
		t.Errorf("poll hit %q, want :fetchPredictOperation", f.lastPath)
	}
	var sent fetchWire
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatalf("poll body not JSON: %v (%s)", err, f.lastBody)
	}
	if sent.OperationName != opName {
		t.Errorf("operationName = %q; the name goes in the BODY, not the path", sent.OperationName)
	}
}

// Veo produces the delivery mode nothing else does: the provider
// writes into an operator-owned bucket. Until now only the mock ever
// returned ArtifactMount.
func TestBucketOutputProducesAMountArtifact(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"done":true,"response":{"videos":[{"gcsUri":"gs://my-bucket/renders/clip.mp4","mimeType":"video/mp4"}]}}`
	d := newDriver(t, f, "gs://my-bucket/renders")

	h, _ := d.Submit(context.Background(), compute.JobRequest{Prompt: "x"})
	st, err := d.Poll(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != compute.JobSucceeded {
		t.Fatalf("status = %q", st.Status)
	}
	if st.Artifact.Kind != compute.ArtifactMount {
		t.Fatalf("kind = %q, want mount", st.Artifact.Kind)
	}
	if st.Artifact.Mount != "my-bucket" || st.Artifact.Path != "renders/clip.mp4" {
		t.Errorf("got %s/%s, want my-bucket/renders/clip.mp4", st.Artifact.Mount, st.Artifact.Path)
	}
}

func TestInlineOutputProducesInlineArtifact(t *testing.T) {
	t.Parallel()
	b64 := base64.StdEncoding.EncodeToString([]byte("MP4BYTES"))
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"done":true,"response":{"videos":[{"bytesBase64Encoded":"` + b64 + `"}]}}`
	d := newDriver(t, f, "")

	h, _ := d.Submit(context.Background(), compute.JobRequest{Prompt: "x"})
	st, err := d.Poll(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Artifact.Kind != compute.ArtifactInline || string(st.Artifact.Bytes) != "MP4BYTES" {
		t.Errorf("artifact = %+v, want decoded inline bytes", st.Artifact)
	}
}

// done is a boolean here, not a status enum. Not-done must not be
// mistaken for a terminal state.
func TestDoneFlagDrivesTerminality(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"done":false}`
	d := newDriver(t, f, "")

	h, _ := d.Submit(context.Background(), compute.JobRequest{Prompt: "x"})
	st, err := d.Poll(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status.Terminal() {
		t.Errorf("status = %q; done:false is not terminal", st.Status)
	}

	f.pollJSON = `{"done":true,"error":{"code":3,"message":"prompt rejected"}}`
	st, err = d.Poll(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != compute.JobFailed || !strings.Contains(st.Err, "prompt rejected") {
		t.Errorf("state = %+v, want failed carrying the reason", st)
	}
}

// done with neither video nor error is the vendor contradicting
// itself. Transient, because another poll may resolve it and the loop
// is bounded by a deadline anyway.
func TestDoneWithNothingIsTransient(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"done":true,"response":{"videos":[]}}`
	d := newDriver(t, f, "")

	h, _ := d.Submit(context.Background(), compute.JobRequest{Prompt: "x"})
	if _, err := d.Poll(context.Background(), h); err == nil {
		t.Fatal("accepted a done operation with no result")
	} else if compute.ClassifyFailure(err) != compute.FailureTransient {
		t.Errorf("classified %s, want transient", compute.ClassifyFailure(err))
	}
}

// The suite that every other driver passes, unchanged. R26's
// acceptance criterion: a suite needing edits to admit the second
// driver is the finding, not the driver.
func TestConformance(t *testing.T) {
	t.Parallel()
	b64 := base64.StdEncoding.EncodeToString([]byte("MP4"))
	f := newFake(t)
	f.submitJSON = `{"name":"` + opName + `"}`
	f.pollJSON = `{"done":true,"response":{"videos":[{"bytesBase64Encoded":"` + b64 + `"}]}}`

	drivertest.RunJob(t, drivertest.JobSubject{
		Name: "veo",
		Job:  newDriver(t, f, ""),
	})
}
