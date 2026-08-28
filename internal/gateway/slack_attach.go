package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// slackFile is one file shared into a conversation.
//
// The download url is url_private, which is NOT public despite living
// on files.slack.com: it needs the bot token as a bearer, and fetching
// it without one returns an HTML sign-in page with HTTP 200. That is
// the trap this file exists to not fall into — the bytes would land on
// disk, the vision builtin would be handed a login page, and the only
// symptom is the model describing a screenshot it never saw.
type slackFile struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Mimetype   string `json:"mimetype,omitempty"`
	Filetype   string `json:"filetype,omitempty"`
	Size       int    `json:"size,omitempty"`
	URLPrivate string `json:"url_private,omitempty"`
}

// slackIncomingDir is where inbound files are materialised. Shared with
// Telegram so a file lands somewhere the vision/audio/pdf builtins
// already look, whichever channel it arrived on.
func (h *SlackHandler) incomingDir() string {
	if d := strings.TrimSpace(h.cfg.IncomingDir); d != "" {
		return d
	}
	return DefaultIncomingDownloadDir
}

// slackFilesToAttachments maps Slack's file metadata onto the
// channel-agnostic shape the agent consumes.
func slackFilesToAttachments(files []slackFile) []types.Attachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]types.Attachment, 0, len(files))
	for _, f := range files {
		if f.URLPrivate == "" {
			continue
		}
		out = append(out, types.Attachment{
			Kind:      slackAttachmentKind(f),
			MimeType:  f.Mimetype,
			Size:      f.Size,
			Reference: f.URLPrivate,
			Filename:  f.Name,
		})
	}
	return out
}

// slackAttachmentKind classifies a file for the agent.
//
// Mimetype first because it is what the modality builtins dispatch on;
// filetype is Slack's own short tag and only consulted when the
// mimetype is missing or uselessly generic.
func slackAttachmentKind(f slackFile) types.AttachmentKind {
	mt := strings.ToLower(f.Mimetype)
	switch {
	case strings.HasPrefix(mt, "image/"):
		return types.AttachmentImage
	case strings.HasPrefix(mt, "audio/"):
		return types.AttachmentAudio
	case strings.HasPrefix(mt, "video/"):
		return types.AttachmentVideo
	}
	switch strings.ToLower(f.Filetype) {
	case "png", "jpg", "jpeg", "gif", "webp", "heic":
		return types.AttachmentImage
	case "mp3", "m4a", "wav", "ogg", "flac":
		return types.AttachmentAudio
	case "mp4", "mov", "webm", "mkv":
		return types.AttachmentVideo
	}
	return types.AttachmentDocument
}

// downloadAttachments fetches each file to <incoming dir>/<turn>/ and
// records the local path.
//
// Best-effort per file, matching the Telegram path: one failure logs
// and skips, and the agent still gets the rest. Returns an error only
// when the directory itself is unwritable, which is a config problem
// rather than a bad attachment.
func (h *SlackHandler) downloadAttachments(ctx context.Context, turnID string, im *IncomingMessage) error {
	if !im.HasMedia() {
		return nil
	}
	turnDir := filepath.Join(h.incomingDir(), turnID)
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		return fmt.Errorf("slack: prep download dir %q: %w", turnDir, err)
	}
	for i := range im.Attachments {
		a := &im.Attachments[i]
		if a.Reference == "" {
			continue
		}
		path, err := h.downloadOne(ctx, turnDir, a)
		if err != nil {
			h.log.Warn("slack: attachment download failed",
				"file", a.Filename, "kind", string(a.Kind), "err", err)
			continue
		}
		a.LocalPath = path
		h.log.Debug("slack: attachment downloaded",
			"file", a.Filename, "kind", string(a.Kind), "path", path, "bytes", a.Size)
	}
	return nil
}

func (h *SlackHandler) downloadOne(ctx context.Context, turnDir string, a *types.Attachment) (string, error) {
	getCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(getCtx, http.MethodGet, a.Reference, nil)
	if err != nil {
		return "", err
	}
	// The bearer is what makes this a download rather than a login
	// page. See slackFile.
	req.Header.Set("Authorization", "Bearer "+h.cfg.BotToken)

	resp, err := h.api.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %w", a.Filename, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %q: HTTP %d", a.Filename, resp.StatusCode)
	}
	// Slack answers an unauthenticated or expired fetch with the
	// sign-in page and a 200, so status alone does not mean bytes.
	// Checking the content type is what turns that into a failure the
	// operator can see rather than a corrupt file the model describes.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(a.MimeType, "text/html") {
		return "", fmt.Errorf("fetch %q: got an HTML page, not the file — the bot token may lack files:read", a.Filename)
	}

	name := a.Filename
	if name == "" {
		name = sanitiseRef(a.Reference) + pickExtension(a)
	}
	dst := filepath.Join(turnDir, filepath.Base(name))

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("create %q: %w", dst, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("write %q: %w", dst, err)
	}
	// Closed explicitly: the final flush can fail after io.Copy has
	// reported success, and swallowing that hands the caller a path to
	// a truncated file. Same reasoning as the Telegram downloader.
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("write %q: %w", dst, err)
	}
	return dst, nil
}
