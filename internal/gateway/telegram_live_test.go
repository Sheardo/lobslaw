package gateway

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// Checks the Bot API accepts the multipart shape this package builds.
// A fake can only confirm the request matches whatever the fake
// expects, which leaves the details that actually break — per-method
// form field names, container handling on sendVoice — unverified.
//
// Two gates: a token AND an explicit LOBSLAW_LIVE_DRIVER_TESTS opt-in,
// so `go test ./...` never posts to somebody's chat by surprise.
// Credentials come from the environment only; a flag would land in
// shell history and CI logs.
//
//	LOBSLAW_LIVE_DRIVER_TESTS=1 LOBSLAW_TEST_CHAT_ID=<id> \
//	  go test -run LiveTelegram ./internal/gateway/
//
// These send real messages. Someone has to look at the chat: a 2xx
// says the upload was accepted, not that it renders as a voice note.

func liveTelegram(t *testing.T) (*TelegramHandler, int64) {
	t.Helper()
	if os.Getenv("LOBSLAW_LIVE_DRIVER_TESTS") == "" {
		t.Skip("LOBSLAW_LIVE_DRIVER_TESTS not set; skipping live Telegram test")
	}
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		t.Skip("TELEGRAM_BOT_TOKEN not set; skipping live Telegram test")
	}
	raw := strings.TrimSpace(os.Getenv("LOBSLAW_TEST_CHAT_ID"))
	if raw == "" {
		t.Skip("LOBSLAW_TEST_CHAT_ID not set; skipping live Telegram test")
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("LOBSLAW_TEST_CHAT_ID %q is not a number", raw)
	}
	h, err := NewTelegramHandler(TelegramConfig{
		BotToken: token,
		Mode:     TelegramModePoll,
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, newAgentFor(t))
	if err != nil {
		t.Fatal(err)
	}
	return h, chatID
}

// realOggOpus produces a genuinely valid file. Telegram inspects the
// container, so bytes merely named .ogg would prove nothing.
func realOggOpus(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; cannot produce a valid OGG/Opus to test sendVoice")
	}
	out := filepath.Join(dir, "probe.ogg")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "libopus", "-b:a", "24k", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not encode opus (%v): %s", err, b)
	}
	return out
}

func fileOpener(path string) ArtifactOpener {
	return func(string) (io.ReadCloser, error) { return os.Open(path) } //nolint:gosec // test-local path
}

// The assertion the fake cannot make: Telegram accepts the multipart
// shape we build, under the field name each method actually expects.
func TestLiveTelegramSendsAVoiceNote(t *testing.T) {
	h, chatID := liveTelegram(t)
	dir := t.TempDir()
	ogg := realOggOpus(t, dir)

	// sendAttachment directly: SendAttachments swallows failures by
	// design, so it would pass whatever happened.
	err := h.sendAttachment(chatID, types.Attachment{
		Kind: types.AttachmentVoice, MimeType: "audio/ogg",
		Reference: "store:probe.ogg", Filename: "probe.ogg",
	}, fileOpener(ogg))
	if err != nil {
		t.Fatalf("Telegram rejected the voice upload: %v", err)
	}
	t.Log("sent a voice note; it should appear in the chat as a playable waveform")
}

// mp3 through sendVoice, which is what the default TTS format
// produces. Telegram renders it as a waveform, so this is the normal
// path rather than an edge case — and if Telegram ever starts
// rejecting non-OGG here, this fails and the routing needs revisiting.
func TestLiveTelegramAcceptsMP3AsVoice(t *testing.T) {
	h, chatID := liveTelegram(t)
	dir := t.TempDir()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	mp3 := filepath.Join(dir, "probe.mp3")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=330:duration=1", mp3)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not encode mp3 (%v): %s", err, b)
	}

	if err := h.sendAttachment(chatID, types.Attachment{
		Kind: types.AttachmentVoice, MimeType: "audio/mpeg",
		Reference: "store:probe.mp3", Filename: "probe.mp3",
	}, fileOpener(mp3)); err != nil {
		t.Fatalf("Telegram rejected an mp3 on sendVoice: %v", err)
	}
	t.Log("sent an mp3 as a voice note; it should appear as a playable waveform")
}
