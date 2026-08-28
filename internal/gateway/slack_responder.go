package gateway

import (
	"context"
	"sync"
)

// slackWorkingText is what a turn says before it has anything to say.
//
// It exists because a Slack turn is otherwise completely silent for
// thirty to ninety seconds. There is no typing indicator available to a
// bot, and without any signal a user cannot tell a working agent from a
// dead one — they re-send, which folds into the same turn, and now two
// messages have produced no visible response at all.
const slackWorkingText = "_Working on it…_"

// slackResponder is the Slack side of the Responder contract, and the
// progress surface for one turn.
//
// The design point: ONE message, rewritten. A placeholder goes up
// immediately, the interim notice rewrites it, and the final reply
// rewrites it again — so the user sees activity the whole way through
// and is left with just the answer. Posting each stage separately would
// leave a trail of "working on it" scaffolding above every reply, which
// is worse in a channel than saying nothing.
type slackResponder struct {
	h       *SlackHandler
	channel string
	// thread is where replies go: a thread in a channel, empty in a DM.
	thread string
	// status is where the native status goes, which is a different
	// question — a DM replies inline but still has an assistant thread,
	// rooted at the user's message. Never empty.
	status string

	// mu guards ts. The interim timer fires on its own goroutine while
	// the turn is running, so the placeholder can be written by one and
	// read by another.
	mu sync.Mutex
	// ts is the placeholder's message id, empty until it is posted and
	// left empty if posting failed — in which case every later stage
	// falls back to posting a new message rather than losing the reply.
	ts string
	// native records that Slack's own assistant status is carrying the
	// progress signal, so no placeholder was posted and none is wanted.
	native bool
}

// slackStatusText is the native status line. Slack renders it as
// "<Bot> is <status>", so this reads as a continuation rather than a
// sentence — "is working on it…", not "Working on it…".
const slackStatusText = "working on it…"

// begin posts the placeholder. Called once, synchronously, before the
// agent runs: the whole point is that it lands immediately rather than
// after the first timer tick.
func (r *slackResponder) begin(ctx context.Context) {
	// Slack's own status first. It is not a message, leaves no trace,
	// and Slack clears it when the reply lands — strictly nicer than
	// anything posted. It needs assistant:write and an assistant
	// thread, so it fails in a plain channel, and that is expected
	// rather than exceptional.
	if err := r.h.api.setAssistantStatus(ctx, r.channel, r.status, slackStatusText); err == nil {
		r.mu.Lock()
		r.native = true
		r.mu.Unlock()
		return
	}

	ts, err := r.h.api.postMessageTS(ctx, r.channel, r.thread, slackWorkingText)
	if err != nil {
		// Soft failure. The turn still runs and still answers; the user
		// just gets the silence this was meant to fill.
		r.h.log.Debug("slack: could not post the working placeholder",
			"channel", r.channel, "err", err)
		return
	}
	r.mu.Lock()
	r.ts = ts
	r.mu.Unlock()
}

func (r *slackResponder) placeholder() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ts
}

func (r *slackResponder) usingNative() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.native
}

// clearStatus removes the native status line.
//
// Slack clears it on the next bot message anyway; doing it explicitly
// closes the window where a reply that failed to post would leave the
// user watching "is working on it…" forever.
func (r *slackResponder) clearStatus(ctx context.Context) {
	if !r.usingNative() {
		return
	}
	if err := r.h.api.setAssistantStatus(ctx, r.channel, r.status, ""); err != nil {
		r.h.log.Debug("slack: could not clear the assistant status", "err", err)
	}
}

// write puts text where the user is already looking: into the
// placeholder if there is one, otherwise as a new message.
func (r *slackResponder) write(ctx context.Context, text string, blocks []any) {
	// The answer supersedes the status, whichever form it took.
	r.clearStatus(ctx)
	if ts := r.placeholder(); ts != "" {
		err := r.h.api.updateMessage(ctx, r.channel, ts, text, blocks)
		if err == nil {
			return
		}
		// An update can fail on a message too old to edit, or one
		// deleted underneath us. Falling through to a fresh post costs
		// a tidy thread and saves the answer.
		r.h.log.Debug("slack: placeholder update failed; posting instead",
			"channel", r.channel, "err", err)
	}
	if blocks != nil {
		if err := r.h.api.postBlocks(ctx, r.channel, r.thread, text, blocks); err != nil {
			r.h.log.Error("slack: block post failed", "channel", r.channel, "err", err)
		}
		return
	}
	r.h.sendText(ctx, r.channel, r.thread, text)
}

// Typing is a no-op: the placeholder is posted once by begin, and a
// repeating presence ping has nothing to ping.
func (r *slackResponder) Typing(context.Context) error { return nil }

// Interim reports mid-turn progress.
//
// Under the native status it REPLACES the status line rather than
// posting anything — which is the whole point of that surface, and
// leaves the thread with one reply and no scaffolding. Falling back, it
// rewrites the placeholder, which is the same idea done with a message.
func (r *slackResponder) Interim(ctx context.Context, text string) error {
	if r.usingNative() {
		if err := r.h.api.setAssistantStatus(ctx, r.channel, r.status, text); err == nil {
			return nil
		}
		// The status worked once and has stopped; say it as a message
		// rather than letting the turn go quiet.
	}
	r.write(ctx, text, nil)
	return nil
}

func (r *slackResponder) Final(ctx context.Context, text string) error {
	r.write(ctx, text, nil)
	return nil
}

// startResponsivenessGuards adapts the handler's config onto the shared
// timers and returns the responder, so the caller can deliver its final
// reply into the same message the progress went to.
//
// The hard timeout matters more here than on Telegram: Slack has
// already been acked, so a turn that never finishes leaves the user
// with silence and no error, and nothing upstream will time it out.
func (h *SlackHandler) startResponsivenessGuards(ctx context.Context, channel, thread, status string) (context.Context, *slackResponder, func()) {
	// status falls back to thread for callers that have only one — the
	// approval resume, which is answering into a thread it was handed.
	if status == "" {
		status = thread
	}
	r := &slackResponder{h: h, channel: channel, thread: thread, status: status}
	r.begin(ctx)
	turnCtx, cleanup := startResponsiveness(ctx, r, ResponsivenessConfig{
		// No typing timer: there is nothing to refresh. The interim
		// timer still runs and rewrites the placeholder at 30s.
		TypingInterval: -1,
		InterimTimeout: h.cfg.InterimTimeout,
		HardTimeout:    h.cfg.HardTimeout,
		Soul:           h.cfg.Soul,
	})
	return turnCtx, r, cleanup
}
