package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The timers used to be Telegram methods, so they were tested — where
// they were tested at all — through a Bot API fake. Written once
// against a fake Responder they can be tested once, which is the point
// of moving them: adding a channel should not mean adding timer tests.

type fakeResponder struct {
	mu      sync.Mutex
	typing  int
	interim []string
	final   []string
}

func (f *fakeResponder) Typing(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	return nil
}

func (f *fakeResponder) Interim(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interim = append(f.interim, text)
	return nil
}

func (f *fakeResponder) Final(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.final = append(f.final, text)
	return nil
}

func (f *fakeResponder) counts() (typing int, interim, final []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typing, append([]string(nil), f.interim...), append([]string(nil), f.final...)
}

// eventually polls until cond holds or the deadline passes. Timer
// tests are inherently about elapsed time; polling keeps them from
// being sleep-for-longer-than-you-need slow.
func eventually(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// Presence has to appear immediately. Waiting one interval before the
// first signal is exactly the window in which a user decides nothing
// is happening.
func TestTypingFiresImmediatelyThenRepeats(t *testing.T) {
	t.Parallel()
	f := &fakeResponder{}
	_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
		TypingInterval: 5 * time.Millisecond,
		InterimTimeout: -1,
		HardTimeout:    time.Minute,
	})
	defer stop()

	if !eventually(t, time.Second, func() bool { n, _, _ := f.counts(); return n >= 1 }) {
		t.Fatal("no typing signal at all")
	}
	if !eventually(t, time.Second, func() bool { n, _, _ := f.counts(); return n >= 3 }) {
		n, _, _ := f.counts()
		t.Errorf("typing fired %d times; it is not repeating", n)
	}
}

// A slow turn says so once. Repeating it does not make the turn
// faster and reads as a stuck loop.
func TestInterimIsSingleShot(t *testing.T) {
	t.Parallel()
	f := &fakeResponder{}
	_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
		TypingInterval: -1,
		InterimTimeout: 5 * time.Millisecond,
		HardTimeout:    time.Minute,
	})
	defer stop()

	if !eventually(t, time.Second, func() bool { _, i, _ := f.counts(); return len(i) == 1 }) {
		t.Fatal("no interim message")
	}
	time.Sleep(50 * time.Millisecond)
	if _, i, _ := f.counts(); len(i) != 1 {
		t.Errorf("interim sent %d times, want exactly 1: %v", len(i), i)
	}
}

// A turn that finishes quickly must say nothing. An interim message
// on a two-second turn is noise.
func TestNothingIsSaidWhenTheTurnIsFast(t *testing.T) {
	t.Parallel()
	f := &fakeResponder{}
	_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
		TypingInterval: -1,
		InterimTimeout: time.Hour,
		HardTimeout:    time.Minute,
	})
	stop() // the turn returned straight away

	time.Sleep(20 * time.Millisecond)
	if _, i, _ := f.counts(); len(i) != 0 {
		t.Errorf("a fast turn announced itself: %v", i)
	}
}

// The hard timeout is the one that matters even where nothing is
// watching: without it a stalled provider hangs the turn forever.
func TestHardTimeoutCancelsTheTurn(t *testing.T) {
	t.Parallel()
	f := &fakeResponder{}
	ctx, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
		TypingInterval: -1,
		InterimTimeout: -1,
		HardTimeout:    10 * time.Millisecond,
	})
	defer stop()

	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("the hard timeout never fired; a stalled turn would hang forever")
	}
}

// A terse personality emitting "still working on this…" reads as a
// different assistant than the one the operator configured.
func TestDirectSoulSkipsInterim(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		directness int
		wantSaid   bool
	}{
		"chatty":            {directness: 3, wantSaid: true},
		"terse":             {directness: 9, wantSaid: false},
		"exactly at cutoff": {directness: directnessChattyCutoff, wantSaid: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			soul := &types.SoulConfig{}
			soul.EmotiveStyle.Directness = tc.directness
			f := &fakeResponder{}
			_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
				TypingInterval: -1,
				InterimTimeout: 5 * time.Millisecond,
				HardTimeout:    time.Minute,
				Soul:           func() *types.SoulConfig { return soul },
			})
			defer stop()

			said := eventually(t, 200*time.Millisecond, func() bool {
				_, i, _ := f.counts()
				return len(i) > 0
			})
			if said != tc.wantSaid {
				t.Errorf("interim sent = %v, want %v at directness %d", said, tc.wantSaid, tc.directness)
			}
		})
	}
}

// A channel with no SOUL wired must not silently lose progress
// reporting — the absence of a personality is not a terse one.
func TestNoSoulStillReportsProgress(t *testing.T) {
	t.Parallel()
	for name, soul := range map[string]func() *types.SoulConfig{
		"nil provider": nil,
		"nil soul":     func() *types.SoulConfig { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeResponder{}
			_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
				TypingInterval: -1,
				InterimTimeout: 5 * time.Millisecond,
				HardTimeout:    time.Minute,
				Soul:           soul,
			})
			defer stop()
			if !eventually(t, 500*time.Millisecond, func() bool { _, i, _ := f.counts(); return len(i) == 1 }) {
				t.Error("no interim message with no SOUL wired")
			}
		})
	}
}

// Cleanup is deferred by every caller and called explicitly by some of
// them. Closing a closed channel would take the process down over a
// tidiness mistake.
func TestCleanupIsIdempotent(t *testing.T) {
	t.Parallel()
	_, stop := startResponsiveness(context.Background(), &fakeResponder{}, ResponsivenessConfig{
		HardTimeout: time.Minute,
	})
	stop()
	stop()
	stop()
}

// A nil responder is a channel with nothing to say back — a webhook.
// It must still get the hard timeout.
func TestNilResponderStillGetsTheTimeout(t *testing.T) {
	t.Parallel()
	ctx, stop := startResponsiveness(context.Background(), nil, ResponsivenessConfig{
		HardTimeout: 10 * time.Millisecond,
	})
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("no hard timeout without a responder")
	}
}

func TestCustomInterimTextIsUsed(t *testing.T) {
	t.Parallel()
	f := &fakeResponder{}
	_, stop := startResponsiveness(context.Background(), f, ResponsivenessConfig{
		TypingInterval: -1,
		InterimTimeout: 5 * time.Millisecond,
		HardTimeout:    time.Minute,
		InterimText:    "hang on",
	})
	defer stop()

	if !eventually(t, 500*time.Millisecond, func() bool { _, i, _ := f.counts(); return len(i) == 1 }) {
		t.Fatal("no interim message")
	}
	if _, i, _ := f.counts(); i[0] != "hang on" {
		t.Errorf("interim text = %q, want the configured one", i[0])
	}
}
