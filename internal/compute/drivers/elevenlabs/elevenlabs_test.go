package elevenlabs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

type fake struct {
	srv *httptest.Server
	// path is the DECODED path; rawURI is the request line as it went
	// on the wire. Containment has to be asserted on rawURI: url.Path
	// decodes %2F back to a slash, so a correctly escaped value looks
	// traversing there when it is not.
	path   string
	rawURI string
	query  string
	hdr    http.Header
	body   []byte
}

func newFake(t *testing.T, status int, audio string) *fake {
	t.Helper()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path, f.rawURI, f.query, f.hdr = r.URL.Path, r.RequestURI, r.URL.RawQuery, r.Header.Clone()
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		f.body = buf
		w.WriteHeader(status)
		_, _ = w.Write([]byte(audio))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newDriver(t *testing.T, f *fake, cfg Config) *Driver {
	t.Helper()
	cfg.BaseURL = f.srv.URL + "/v1/text-to-speech/"
	if cfg.Credential == nil {
		cfg.Credential = compute.NewHeaderCredential("xi-api-key", "k")
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The point of a second speak driver: it must disagree with the first
// everywhere that matters, and still satisfy the same interface. If
// these assertions could be made of the OpenAI driver too, this
// driver is not testing anything.
func TestDisagreesWithTheOpenAIShape(t *testing.T) {
	t.Parallel()
	f := newFake(t, http.StatusOK, "MP3BYTES")
	d := newDriver(t, f, Config{})

	art, err := d.Speak(context.Background(), compute.SpeakRequest{
		Text: "hello", Voice: "voice-abc",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Voice in the path, not the body.
	if !strings.HasSuffix(f.path, "/voice-abc") {
		t.Errorf("path = %q, want the voice as a path segment", f.path)
	}
	var sent wireRequest
	if err := json.Unmarshal(f.body, &sent); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, f.body)
	}
	if strings.Contains(string(f.body), "voice-abc") {
		t.Error("voice leaked into the body; this API takes it in the path")
	}

	// 2. xi-api-key, not Authorization.
	if got := f.hdr.Get("xi-api-key"); got != "k" {
		t.Errorf("xi-api-key = %q, want k", got)
	}
	if got := f.hdr.Get("Authorization"); got != "" {
		t.Errorf("sent Authorization (%q); this API uses xi-api-key", got)
	}

	// 3. Format is a query parameter naming codec AND rate.
	if !strings.Contains(f.query, "output_format=mp3_44100_128") {
		t.Errorf("query = %q, want an output_format naming codec and rate", f.query)
	}

	// 4. model_id selects the engine, distinct from the voice.
	if sent.ModelID != DefaultModel {
		t.Errorf("model_id = %q, want %q", sent.ModelID, DefaultModel)
	}

	if art.Kind != compute.ArtifactInline || string(art.Bytes) != "MP3BYTES" {
		t.Errorf("artifact = %+v, want inline audio", art)
	}
}

// A voice reaches this driver from a tool argument the model chose. A
// value containing a slash would address a different endpoint, so it
// is escaped rather than concatenated.
func TestVoiceIsPathEscaped(t *testing.T) {
	t.Parallel()
	f := newFake(t, http.StatusOK, "AUDIO")
	d := newDriver(t, f, Config{})

	if _, err := d.Speak(context.Background(), compute.SpeakRequest{
		Text: "x", Voice: "../../admin/keys",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.rawURI, "/admin/keys") {
		t.Errorf("wire path = %q; a traversing voice reached a different endpoint", f.rawURI)
	}
	if !strings.Contains(f.rawURI, "%2F") {
		t.Errorf("wire path = %q; the slashes were not escaped", f.rawURI)
	}
}

// Container names the tool exposes map onto the vendor's
// codec_rate spelling, and the MIME must follow the codec actually
// requested or the file lands with the wrong extension.
func TestFormatTranslationDrivesMIME(t *testing.T) {
	t.Parallel()
	for format, want := range map[string]string{
		"mp3":  "audio/mpeg",
		"opus": "audio/ogg",
		"wav":  "audio/wav",
	} {
		f := newFake(t, http.StatusOK, "AUDIO")
		d := newDriver(t, f, Config{})
		art, err := d.Speak(context.Background(), compute.SpeakRequest{Text: "x", Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if art.MIME != want {
			t.Errorf("format %q → MIME %q, want %q (query was %q)", format, art.MIME, want, f.query)
		}
	}
}

// Same taxonomy as every other driver on the waist. A second vendor
// that classified differently would make the failover chain behave
// differently depending on which provider happened to be first.
func TestClassifiesFailuresLikeEveryOtherDriver(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		body   string
		want   compute.FailureClass
	}{
		{503, `{"detail":"upstream"}`, compute.FailureTransient},
		{401, `{"detail":"bad key"}`, compute.FailurePermanent},
		{422, `{"detail":"unknown voice"}`, compute.FailurePermanent},
	} {
		f := newFake(t, tc.status, tc.body)
		d := newDriver(t, f, Config{})
		_, err := d.Speak(context.Background(), compute.SpeakRequest{Text: "x"})
		if err == nil {
			t.Fatalf("HTTP %d produced no error", tc.status)
		}
		if got := compute.ClassifyFailure(err); got != tc.want {
			t.Errorf("HTTP %d classified %s, want %s", tc.status, got, tc.want)
		}
	}
}

func TestRequiresCredential(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Error("constructed a driver with no credential")
	}
}

// A voice is required by the API, so an unconfigured driver defaults
// rather than 404ing on the first call.
func TestDefaultsAVoice(t *testing.T) {
	t.Parallel()
	f := newFake(t, http.StatusOK, "AUDIO")
	d := newDriver(t, f, Config{})
	if _, err := d.Speak(context.Background(), compute.SpeakRequest{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.path, "/"+DefaultVoice) {
		t.Errorf("path = %q, want the default voice", f.path)
	}
}
