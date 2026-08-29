package gateway

import (
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Every file upload arrives as subtype "file_share". Rejecting all
// subtypes — which is right for edits, joins and deletions — would
// mean the bot silently ignores every screenshot anybody sends it.
func TestSlackFileShareIsWanted(t *testing.T) {
	t.Parallel()

	h := &SlackHandler{botUserID: "U0BOT"}

	shared := slackEvent{
		Type:    "message",
		Subtype: "file_share",
		User:    "U0ALICE",
		Files:   []slackFile{{ID: "F1", URLPrivate: "https://files.slack.com/x", Mimetype: "image/png"}},
	}
	if !h.wantsEvent(shared) {
		t.Fatal("a file upload was filtered out")
	}

	// With no comment at all — the common case for "look at this".
	if !h.wantsEvent(slackEvent{
		Type: "message", Subtype: "file_share", User: "U0ALICE", Text: "",
		Files: []slackFile{{ID: "F1", URLPrivate: "https://files.slack.com/x"}},
	}) {
		t.Fatal("a file with no comment was filtered out")
	}

	// Other subtypes stay rejected.
	if h.wantsEvent(slackEvent{Type: "message", Subtype: "message_changed", User: "U0ALICE", Text: "hi"}) {
		t.Error("an edit was accepted")
	}
	// And a file share from the bot itself must not loop.
	if h.wantsEvent(slackEvent{
		Type: "message", Subtype: "file_share", User: "U0BOT",
		Files: []slackFile{{ID: "F1", URLPrivate: "x"}},
	}) {
		t.Error("the bot's own file share was accepted")
	}
}

func TestSlackFilesToAttachments(t *testing.T) {
	t.Parallel()

	got := slackFilesToAttachments([]slackFile{
		{ID: "F1", Name: "shot.png", Mimetype: "image/png", Size: 42, URLPrivate: "https://files.slack.com/a"},
		{ID: "F2", Name: "no-url.png", Mimetype: "image/png"}, // skipped: nothing to fetch
	})
	if len(got) != 1 {
		t.Fatalf("got %d attachments, want 1", len(got))
	}
	a := got[0]
	if a.Kind != types.AttachmentImage {
		t.Errorf("kind = %v, want image", a.Kind)
	}
	// Reference is the download url, not the file id: that is what the
	// downloader fetches.
	if a.Reference != "https://files.slack.com/a" {
		t.Errorf("reference = %q, want the private url", a.Reference)
	}
	if a.Filename != "shot.png" || a.Size != 42 {
		t.Errorf("metadata lost: %+v", a)
	}
}

// Mimetype decides, because that is what the modality builtins
// dispatch on. Filetype is the fallback for a file Slack did not
// classify.
func TestSlackAttachmentKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		file slackFile
		want types.AttachmentKind
	}{
		{slackFile{Mimetype: "image/png"}, types.AttachmentImage},
		{slackFile{Mimetype: "audio/mpeg"}, types.AttachmentAudio},
		{slackFile{Mimetype: "video/mp4"}, types.AttachmentVideo},
		{slackFile{Mimetype: "application/pdf"}, types.AttachmentDocument},
		{slackFile{Filetype: "png"}, types.AttachmentImage},
		{slackFile{Filetype: "m4a"}, types.AttachmentAudio},
		{slackFile{Filetype: "mov"}, types.AttachmentVideo},
		{slackFile{}, types.AttachmentDocument},
		// Mimetype wins over a contradicting filetype.
		{slackFile{Mimetype: "image/png", Filetype: "txt"}, types.AttachmentImage},
	}
	for _, tc := range cases {
		if got := slackAttachmentKind(tc.file); got != tc.want {
			t.Errorf("%+v → %v, want %v", tc.file, got, tc.want)
		}
	}
}

func TestSlackIncomingDirDefaults(t *testing.T) {
	t.Parallel()

	if got := (&SlackHandler{}).incomingDir(); got != DefaultIncomingDownloadDir {
		t.Errorf("default = %q, want %q", got, DefaultIncomingDownloadDir)
	}
	h := &SlackHandler{cfg: SlackConfig{IncomingDir: "/tmp/x"}}
	if got := h.incomingDir(); got != "/tmp/x" {
		t.Errorf("configured = %q", got)
	}
}
