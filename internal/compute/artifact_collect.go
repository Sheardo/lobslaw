package compute

import (
	"context"
	"sync"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A tool that produces a FILE has a delivery problem the other tools
// do not. Text goes back in the tool result and the model reads it;
// audio cannot. The model gets a path, and something else has to put
// the actual bytes in front of the user.
//
// This is that channel. Tools announce what they produced, the agent
// collects it, and the channel layer attaches it alongside the reply.
//
// It is a turn-scoped collector rather than the agent parsing tool
// output because sniffing every tool's JSON for an artifact-shaped
// object is guesswork that breaks the moment a tool legitimately
// returns a field called "path". Announcing is explicit and typed.

type artifactCollectorKey struct{}

// ArtifactCollector gathers the files produced during one turn.
type ArtifactCollector struct {
	mu   sync.Mutex
	refs []types.Attachment
}

// WithArtifactCollector attaches a fresh collector to ctx. The agent
// calls this once per turn and reads the result at the end.
func WithArtifactCollector(ctx context.Context) (context.Context, *ArtifactCollector) {
	c := &ArtifactCollector{}
	return context.WithValue(ctx, artifactCollectorKey{}, c), c
}

// CollectArtifact announces a produced file.
//
// A no-op when no collector is present, which is deliberate: builtins
// are called from tests, from the scheduler and from turns that have
// no channel to deliver to, and none of those should have to care.
func CollectArtifact(ctx context.Context, a types.Attachment) {
	c, ok := ctx.Value(artifactCollectorKey{}).(*ArtifactCollector)
	if !ok || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refs = append(c.refs, a)
}

// Collected returns what the turn produced, in the order produced.
func (c *ArtifactCollector) Collected() []types.Attachment {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copied: the caller keeps this past the turn, and tools may still
	// be finishing in another goroutine.
	out := make([]types.Attachment, len(c.refs))
	copy(out, c.refs)
	return out
}

// AttachmentKindForMIME maps a produced file onto the kind the
// channel layer switches on. Voice notes and plain audio differ in
// how they present — a voice note plays inline — so speech is
// deliberately AttachmentVoice rather than AttachmentAudio.
func AttachmentKindForMIME(mime string) types.AttachmentKind {
	switch {
	case mime == "audio/ogg", mime == "audio/mpeg", mime == "audio/wav":
		return types.AttachmentVoice
	case len(mime) > 6 && mime[:6] == "audio/":
		return types.AttachmentAudio
	case len(mime) > 6 && mime[:6] == "image/":
		return types.AttachmentImage
	case len(mime) > 6 && mime[:6] == "video/":
		return types.AttachmentVideo
	default:
		return types.AttachmentDocument
	}
}
