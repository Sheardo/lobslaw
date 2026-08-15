package gateway

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

type capturedUpload struct {
	method   string
	field    string
	filename string
	body     string
	chatID   string
}

// fakeBotAPI records what was uploaded, so the tests assert on the
// Bot API call rather than on our own intent.
func fakeBotAPI(t *testing.T, status int) (*httptest.Server, *[]capturedUpload) {
	t.Helper()
	var mu sync.Mutex
	var got []capturedUpload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := capturedUpload{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(part)
				switch part.FormName() {
				case "chat_id":
					up.chatID = string(b)
				default:
					up.field = part.FormName()
					up.filename = part.FileName()
					up.body = string(b)
				}
			}
		}
		mu.Lock()
		got = append(got, up)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func attachHandler(t *testing.T, srvURL string) *TelegramHandler {
	t.Helper()
	h, err := NewTelegramHandler(TelegramConfig{BotToken: "t", Mode: TelegramModePoll, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, newAgentFor(t))
	if err != nil {
		t.Fatal(err)
	}
	h.base = srvURL
	return h
}

func openerFor(body string) ArtifactOpener {
	return func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
}

// The point of the whole change: audio a turn generated has to reach
// the user as bytes, not as a sentence describing a path.
func TestSendAttachmentsUploadsTheFile(t *testing.T) {
	t.Parallel()
	srv, got := fakeBotAPI(t, http.StatusOK)
	h := attachHandler(t, srv.URL)

	h.SendAttachments(42, []types.Attachment{{
		Kind: types.AttachmentVoice, MimeType: "audio/ogg",
		Reference: "store:generated/hello.ogg", Filename: "hello.ogg",
	}}, openerFor("OGGDATA"))

	if len(*got) != 1 {
		t.Fatalf("uploads = %d, want 1", len(*got))
	}
	up := (*got)[0]
	if up.method != "sendVoice" || up.field != "voice" {
		t.Errorf("method/field = %s/%s, want sendVoice/voice", up.method, up.field)
	}
	if up.body != "OGGDATA" {
		t.Errorf("uploaded %q, want the artifact bytes", up.body)
	}
	if up.chatID != "42" {
		t.Errorf("chat_id = %q, want 42", up.chatID)
	}
	if up.filename != "hello.ogg" {
		t.Errorf("filename = %q", up.filename)
	}
}

// Voice goes to sendVoice whatever the container. Telegram renders an
// mp3 sent this way as a waveform, identically to an OGG; routing it
// to sendAudio instead produces a file row with a filename, which is
// the wrong thing for someone who asked to hear something.
// TestLiveTelegramAcceptsMP3AsVoice is the check against the real API.
func TestVoiceAlwaysUsesSendVoice(t *testing.T) {
	t.Parallel()
	for _, mimeType := range []string{"audio/ogg", "audio/mpeg", "audio/wav"} {
		srv, got := fakeBotAPI(t, http.StatusOK)
		h := attachHandler(t, srv.URL)

		h.SendAttachments(1, []types.Attachment{{
			Kind: types.AttachmentVoice, MimeType: mimeType,
			Reference: "store:a", Filename: "a",
		}}, openerFor("AUDIO"))

		if len(*got) != 1 || (*got)[0].method != "sendVoice" {
			t.Errorf("%s → %+v, want a single sendVoice", mimeType, *got)
		}
	}
}

func TestAttachmentKindPicksTheMethod(t *testing.T) {
	t.Parallel()
	for kind, want := range map[types.AttachmentKind]string{
		types.AttachmentImage:    "sendPhoto",
		types.AttachmentVideo:    "sendVideo",
		types.AttachmentDocument: "sendDocument",
		types.AttachmentAudio:    "sendAudio",
	} {
		srv, got := fakeBotAPI(t, http.StatusOK)
		h := attachHandler(t, srv.URL)
		h.SendAttachments(1, []types.Attachment{{Kind: kind, Reference: "store:f"}}, openerFor("x"))
		if len(*got) != 1 || (*got)[0].method != want {
			t.Errorf("kind %s → %+v, want %s", kind, *got, want)
		}
	}
}

// One bad attachment must not take the others with it. The reply text
// has already been sent, so a failure here costs a file, not the answer.
func TestOneFailedAttachmentDoesNotBlockTheRest(t *testing.T) {
	t.Parallel()
	srv, got := fakeBotAPI(t, http.StatusOK)
	h := attachHandler(t, srv.URL)

	var calls int
	open := func(ref string) (io.ReadCloser, error) {
		calls++
		if strings.Contains(ref, "missing") {
			return nil, errors.New("no such file")
		}
		return io.NopCloser(strings.NewReader("DATA")), nil
	}
	h.SendAttachments(1, []types.Attachment{
		{Kind: types.AttachmentAudio, Reference: "store:missing.mp3"},
		{Kind: types.AttachmentAudio, Reference: "store:present.mp3"},
	}, open)

	if calls != 2 {
		t.Errorf("opener called %d times, want 2 — the second attachment was skipped", calls)
	}
	if len(*got) != 1 {
		t.Errorf("uploads = %d, want 1 (the readable one)", len(*got))
	}
}

// With no opener the files cannot be delivered. Saying so beats
// pretending the turn succeeded in full.
func TestNoOpenerIsReportedNotSilent(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	h, err := NewTelegramHandler(TelegramConfig{
		BotToken: "t",
		Mode:     TelegramModePoll,
		Logger:   slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}, newAgentFor(t))
	if err != nil {
		t.Fatal(err)
	}
	h.SendAttachments(1, []types.Attachment{{Kind: types.AttachmentAudio, Reference: "store:a"}}, nil)
	if !strings.Contains(buf.String(), "no artifact opener") {
		t.Errorf("dropped attachments without warning; log was: %s", buf.String())
	}
}
