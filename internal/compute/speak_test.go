package compute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func speakServer(t *testing.T, status int, body []byte, record *speakWire) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if record != nil {
			_ = json.NewDecoder(r.Body).Decode(record)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSpeakDriver(t *testing.T, endpoint string) *OpenAISpeakDriver {
	t.Helper()
	d, err := NewOpenAISpeakDriver(OpenAISpeakConfig{
		Endpoint:   endpoint,
		Model:      "tts-1",
		Voice:      "alloy",
		Credential: NewBearerCredential("k"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Audio comes back as an Artifact, not bytes, so a vendor that later
// returns a URL or writes into a bucket fits without changing the
// interface. Today it is inline, and the MIME has to match the format
// actually requested or the file lands with the wrong extension.
func TestSpeakReturnsAnInlineArtifact(t *testing.T) {
	t.Parallel()
	var sent speakWire
	srv := speakServer(t, http.StatusOK, []byte("ID3-fake-mp3"), &sent)

	art, err := newSpeakDriver(t, srv.URL).Speak(context.Background(), SpeakRequest{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if art.Kind != ArtifactInline || string(art.Bytes) != "ID3-fake-mp3" {
		t.Errorf("artifact = %+v, want inline audio bytes", art)
	}
	if art.MIME != "audio/mpeg" {
		t.Errorf("MIME = %q, want audio/mpeg for the default mp3 format", art.MIME)
	}
	if sent.Input != "hello" || sent.Model != "tts-1" || sent.Voice != "alloy" {
		t.Errorf("request did not carry the configured defaults: %+v", sent)
	}
	if sent.ResponseFormat != DefaultSpeakFormat {
		t.Errorf("response_format = %q, want %q", sent.ResponseFormat, DefaultSpeakFormat)
	}
}

func TestSpeakFormatDrivesMIME(t *testing.T) {
	t.Parallel()
	for format, want := range map[string]string{
		"wav": "audio/wav", "opus": "audio/ogg", "flac": "audio/flac", "mp3": "audio/mpeg",
	} {
		srv := speakServer(t, http.StatusOK, []byte("audio"), nil)
		art, err := newSpeakDriver(t, srv.URL).Speak(context.Background(),
			SpeakRequest{Text: "x", Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if art.MIME != want {
			t.Errorf("format %q → MIME %q, want %q", format, art.MIME, want)
		}
	}
}

// The failover chain reads the class, so speak has to classify like
// every other driver on the waist.
func TestSpeakClassifiesFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   FailureClass
	}{
		{"server error", 503, `{"error":"upstream"}`, FailureTransient},
		{"quota", 402, `{"error":"credit balance is too low"}`, FailureQuotaExhausted},
		{"bad voice", 400, `{"error":"unknown voice"}`, FailurePermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := speakServer(t, tc.status, []byte(tc.body), nil)
			_, err := newSpeakDriver(t, srv.URL).Speak(context.Background(), SpeakRequest{Text: "x"})
			if err == nil {
				t.Fatalf("HTTP %d produced no error", tc.status)
			}
			if got := ClassifyFailure(err); got != tc.want {
				t.Errorf("HTTP %d classified %s, want %s", tc.status, got, tc.want)
			}
		})
	}
}

// A 200 carrying no audio is a provider bug rather than a caller one,
// so it is worth trying the backup rather than surfacing as success.
func TestSpeakTreatsEmptyAudioAsTransient(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, nil, nil)
	_, err := newSpeakDriver(t, srv.URL).Speak(context.Background(), SpeakRequest{Text: "x"})
	if err == nil {
		t.Fatal("an empty response was accepted as audio")
	}
	if got := ClassifyFailure(err); got != FailureTransient {
		t.Errorf("classified %s, want transient", got)
	}
}

// Empty text fails identically everywhere, so failing over would just
// spend the backup's quota to learn the same thing.
func TestSpeakRejectsEmptyTextPermanently(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, []byte("audio"), nil)
	_, err := newSpeakDriver(t, srv.URL).Speak(context.Background(), SpeakRequest{Text: "   "})
	if err == nil {
		t.Fatal("empty text was sent to the provider")
	}
	if got := ClassifyFailure(err); got != FailurePermanent {
		t.Errorf("classified %s, want permanent", got)
	}
}

// --- the builtin -----------------------------------------------------

func speakBuiltin(t *testing.T, srvURL string, maxChars int) (BuiltinFunc, string) {
	t.Helper()
	root := t.TempDir()
	b := NewBuiltins()
	if err := RegisterSpeakBuiltin(b, SpeakConfig{
		Driver:   newSpeakDriver(t, srvURL),
		Resolver: &ArtifactResolver{Mounts: fakeMounts{label: "store", root: root}, DefaultMount: "store"},
		MaxChars: maxChars,
	}); err != nil {
		t.Fatal(err)
	}
	h, ok := b.Get("speak")
	if !ok {
		t.Fatal("speak not registered")
	}
	return h, root
}

// What the model gets back is a PATH, not audio. Bytes in a tool
// result would be unreadable to the model and enormous in context.
func TestSpeakBuiltinReturnsAPath(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, []byte("ID3-fake"), nil)
	h, root := speakBuiltin(t, srv.URL, 0)

	out, code, err := h(context.Background(), map[string]string{"text": "Hello there world"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var got struct{ Mount, Path, MIME string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, out)
	}
	if got.Mount != "store" {
		t.Errorf("mount = %q, want store", got.Mount)
	}
	if !strings.HasSuffix(got.Path, ".mp3") {
		t.Errorf("path %q has no audio extension", got.Path)
	}
	// A readable name makes a mount of generated audio browsable.
	if !strings.Contains(got.Path, "hello") {
		t.Errorf("path %q does not reflect the spoken text", got.Path)
	}
	if _, err := os.Stat(filepath.Join(root, got.Path)); err != nil {
		t.Errorf("audio not written: %v", err)
	}
	if strings.Contains(string(out), "ID3-fake") {
		t.Error("the audio bytes were returned to the model instead of a path")
	}
}

// TTS is billed per character, so an over-long passage is refused
// with an argument error the model can act on rather than truncated
// into audio that stops mid-sentence.
func TestSpeakBuiltinRefusesOverlongText(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, []byte("audio"), nil)
	h, _ := speakBuiltin(t, srv.URL, 10)

	_, code, err := h(context.Background(), map[string]string{"text": strings.Repeat("a", 50)})
	if err == nil {
		t.Fatal("an over-long passage was synthesised anyway")
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (bad argument the model can fix)", code)
	}
	if !strings.Contains(err.Error(), "limit is 10") {
		t.Errorf("error should state the limit, got: %v", err)
	}
}

func TestSpeakBuiltinRequiresText(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, []byte("audio"), nil)
	h, _ := speakBuiltin(t, srv.URL, 0)
	if _, code, err := h(context.Background(), map[string]string{}); err == nil || code != 2 {
		t.Errorf("missing text: code=%d err=%v, want an argument error", code, err)
	}
}

// A speak tool with nowhere to write would bill for synthesis and
// then drop the result, so construction fails rather than registering
// a tool that cannot succeed.
func TestSpeakBuiltinRequiresAResolver(t *testing.T) {
	t.Parallel()
	srv := speakServer(t, http.StatusOK, []byte("audio"), nil)
	if err := RegisterSpeakBuiltin(NewBuiltins(), SpeakConfig{
		Driver: newSpeakDriver(t, srv.URL),
	}); err == nil {
		t.Error("registered a speak tool with no artifact resolver")
	}
}
