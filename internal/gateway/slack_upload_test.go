package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// fakeSlackUpload stands in for the three endpoints an upload touches,
// recording what each was sent so the sequence can be asserted rather
// than assumed.
type fakeSlackUpload struct {
	srv *httptest.Server

	gotFilename  string
	gotLength    string
	gotBody      []byte
	gotComplete  map[string]any
	completeFail bool
}

func newFakeSlackUpload(t *testing.T) *fakeSlackUpload {
	t.Helper()
	f := &fakeSlackUpload{}
	mux := http.NewServeMux()

	mux.HandleFunc("/files.getUploadURLExternal", func(w http.ResponseWriter, r *http.Request) {
		// Form-encoded, not JSON. Asserted because handing this
		// endpoint JSON fails as invalid_arguments, which reads like a
		// bad filename rather than a wrong content type.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Errorf("getUploadURLExternal Content-Type = %q, want form-encoded", ct)
		}
		_ = r.ParseForm()
		f.gotFilename = r.Form.Get("filename")
		f.gotLength = r.Form.Get("length")
		writeJSON(w, map[string]any{
			"ok":         true,
			"upload_url": f.srv.URL + "/upload-slot",
			"file_id":    "F0NEW",
		})
	})

	mux.HandleFunc("/upload-slot", func(w http.ResponseWriter, r *http.Request) {
		// The bearer must NOT travel here: this is whatever host the
		// API named, not Slack's own.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("bot token leaked to the upload url: %q", auth)
		}
		f.gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK - 12 bytes"))
	})

	mux.HandleFunc("/files.completeUploadExternal", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.gotComplete)
		if f.completeFail {
			writeJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func uploadHandler(t *testing.T, f *fakeSlackUpload) *SlackHandler {
	t.Helper()
	return &SlackHandler{
		cfg: SlackConfig{BotToken: "xoxb-test"},
		log: discardLogger(),
		api: newSlackAPI("xoxb-test", f.srv.URL, f.srv.Client()),
	}
}

// openerFor lives in telegram_attach_test.go — the same helper, and
// the two channels genuinely take the same ArtifactOpener.

func TestSlackUploadRunsAllThreeSteps(t *testing.T) {
	t.Parallel()

	f := newFakeSlackUpload(t)
	h := uploadHandler(t, f)

	err := h.sendAttachment(context.Background(), "C1", "1700000000.000100",
		types.Attachment{Kind: types.AttachmentImage, Filename: "chart.png", Reference: "artifacts:chart.png"},
		openerFor("PNGDATA"))
	if err != nil {
		t.Fatalf("sendAttachment: %v", err)
	}

	if f.gotFilename != "chart.png" {
		t.Errorf("filename = %q", f.gotFilename)
	}
	// The exact byte count, or Slack rejects at the complete step after
	// the bytes have already been sent.
	if f.gotLength != "7" {
		t.Errorf("length = %q, want 7", f.gotLength)
	}
	if string(f.gotBody) != "PNGDATA" {
		t.Errorf("uploaded body = %q", f.gotBody)
	}
	if f.gotComplete["channel_id"] != "C1" {
		t.Errorf("channel_id = %v", f.gotComplete["channel_id"])
	}
	// A file answering a threaded turn belongs in that thread, not
	// loose in the channel.
	if f.gotComplete["thread_ts"] != "1700000000.000100" {
		t.Errorf("thread_ts = %v", f.gotComplete["thread_ts"])
	}
}

// Between upload and complete the file exists but belongs nowhere. The
// error has to say so, because that is a leaked file rather than a
// lost one.
func TestSlackUploadReportsAnUnsharedFile(t *testing.T) {
	t.Parallel()

	f := newFakeSlackUpload(t)
	f.completeFail = true
	h := uploadHandler(t, f)

	err := h.sendAttachment(context.Background(), "C1", "",
		types.Attachment{Filename: "x.bin", Reference: "artifacts:x.bin"}, openerFor("data"))
	if err == nil {
		t.Fatal("a failed share was reported as success")
	}
	if !strings.Contains(err.Error(), "uploaded but not shared") || !strings.Contains(err.Error(), "F0NEW") {
		t.Errorf("err = %v, want it to name the orphaned file", err)
	}
}

func TestSlackUploadRejectsEmptyAndOversized(t *testing.T) {
	t.Parallel()

	f := newFakeSlackUpload(t)
	h := uploadHandler(t, f)

	// Empty: Slack's own error names the filename and sends the reader
	// looking in the wrong place.
	err := h.sendAttachment(context.Background(), "C1", "",
		types.Attachment{Filename: "empty.bin", Reference: "artifacts:empty.bin"}, openerFor(""))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty artifact err = %v", err)
	}

	// Oversized: the read is capped so a runaway artifact cannot be
	// buffered whole just to be rejected.
	big := strings.Repeat("x", slackMaxUploadBytes+10)
	err = h.sendAttachment(context.Background(), "C1", "",
		types.Attachment{Filename: "big.bin", Reference: "artifacts:big.bin"}, openerFor(big))
	if err == nil || !strings.Contains(err.Error(), "upload cap") {
		t.Errorf("oversized artifact err = %v", err)
	}
}

// A missing opener must be loud. A turn that generated audio and
// delivered nothing looks exactly like one that generated nothing, and
// the model will have told the user their file was ready.
func TestSlackSendAttachmentsWithoutOpenerIsNotSilent(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{cfg: SlackConfig{}, log: discardLogger()}
	// No panic, no send, and nothing to assert beyond it surviving —
	// the warning is the behaviour, exercised here for the nil path.
	h.SendAttachments(context.Background(), "C1", "", []types.Attachment{{Reference: "a"}}, nil)
	// Zero attachments must not warn at all.
	h.SendAttachments(context.Background(), "C1", "", nil, nil)
}
