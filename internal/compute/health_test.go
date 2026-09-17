package compute

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A failover chain trying every provider on every turn is right the
// first time and wasteful the hundredth. These are about the second
// case, and about not over-correcting into a chain that has written
// off everything and replies to nobody.

func atFixed(h *ProviderHealth, t time.Time) { h.now = func() time.Time { return t } }

func TestUntriedProviderIsHealthy(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	if !h.Available("never-seen") {
		t.Error("a provider nobody has failed against was reported unhealthy")
	}
	// A nil tracker must not be pessimistic either — a caller with none
	// wired behaves exactly as before health tracking existed.
	var nilHealth *ProviderHealth
	if !nilHealth.Available("anything") {
		t.Error("a nil tracker reported a provider unhealthy")
	}
	nilHealth.RecordFailure("anything", FailureTransient) // must not panic
	nilHealth.RecordSuccess("anything")
}

func TestCredentialFailureGetsALongCooldown(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	start := time.Unix(1_000_000, 0)
	atFixed(h, start)

	h.RecordFailure("openai", FailureCredential)
	if h.Available("openai") {
		t.Fatal("a provider that rejected the credential is still first in the chain")
	}

	// Nothing about a wrong key improves in thirty seconds.
	atFixed(h, start.Add(time.Minute))
	if h.Available("openai") {
		t.Error("credential cooldown expired after a minute; the key has not changed")
	}
	atFixed(h, start.Add(cooldownCredential+time.Second))
	if !h.Available("openai") {
		t.Error("credential cooldown never expires; a fixed key would never be retried")
	}
}

// Transient failures back off, so a dead provider is not retried every
// thirty seconds forever — but never past the cap, or a chain that has
// written everything off replies to nobody.
func TestTransientFailuresBackOffButStayBounded(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	start := time.Unix(1_000_000, 0)
	atFixed(h, start)

	h.RecordFailure("flaky", FailureTransient)
	first := h.CooldownRemaining("flaky")
	if first != cooldownTransient {
		t.Errorf("first cooldown = %v, want %v", first, cooldownTransient)
	}

	h.RecordFailure("flaky", FailureTransient)
	if second := h.CooldownRemaining("flaky"); second <= first {
		t.Errorf("second cooldown %v did not grow past the first %v", second, first)
	}

	for range 20 {
		h.RecordFailure("flaky", FailureTransient)
	}
	if got := h.CooldownRemaining("flaky"); got > maxCooldown {
		t.Errorf("cooldown = %v, want no more than %v — a dead provider must still be retried sometimes",
			got, maxCooldown)
	}
}

// A 400 is a property of the request, not of the provider. Demoting on
// one would let a single malformed turn take a healthy provider out of
// the chain for everybody else.
func TestPermanentFailureDoesNotDemote(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	h.RecordFailure("openai", FailurePermanent)
	if !h.Available("openai") {
		t.Error("a bad request demoted the provider that correctly rejected it")
	}
}

// One success is enough. Requiring several keeps a recovered provider
// out of the chain for no reason, and being wrong costs one attempt.
func TestSuccessClearsTheDemotion(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	start := time.Unix(1_000_000, 0)
	atFixed(h, start)

	h.RecordFailure("openai", FailureCredential)
	h.RecordSuccess("openai")
	if !h.Available("openai") {
		t.Error("a recovered provider is still demoted")
	}

	// And the backoff counter resets with it: a provider that had a bad
	// minute last week must not start next week's first failure at a
	// five-minute cooldown.
	h.RecordFailure("openai", FailureTransient)
	if got := h.CooldownRemaining("openai"); got != cooldownTransient {
		t.Errorf("cooldown after recovery = %v, want the first-failure value %v", got, cooldownTransient)
	}
}

func TestDemotedListsWhatIsBeingSkipped(t *testing.T) {
	t.Parallel()
	h := NewProviderHealth()
	start := time.Unix(1_000_000, 0)
	atFixed(h, start)

	h.RecordFailure("openai", FailureCredential)
	h.RecordFailure("anthropic", FailureTransient)

	got := h.Demoted()
	if len(got) != 2 {
		t.Fatalf("Demoted() = %+v, want both providers", got)
	}
	if got["openai"].Class != FailureCredential {
		t.Errorf("openai class = %v, want credential — an operator needs to know which fault it was",
			got["openai"].Class)
	}

	// Past the longest cooldown, which is the credential one.
	atFixed(h, start.Add(cooldownCredential+time.Minute))
	if len(h.Demoted()) != 0 {
		t.Errorf("expired demotions are still listed: %+v", h.Demoted())
	}
}

// The classification change this all rests on. A 401 used to be
// permanent, which aborted the chain — one rotated key took the
// assistant down while two working providers sat idle.
func TestRejectedCredentialAdvancesTheChain(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		class := ClassifyHTTPStatus(status, `{"error":"bad key"}`)
		if class != FailureCredential {
			t.Errorf("HTTP %d classified %s, want credential-rejected", status, class)
		}
		err := &DriverError{Class: class, Err: errors.New("auth failed")}
		if !IsRetryableProviderError(t.Context(), err) {
			t.Errorf("HTTP %d does not advance the chain; one stale key takes the assistant down", status)
		}
	}
}

// ...but a 400 still must not. Advancing on one would multiply a
// single clear error into one per provider and report the last.
func TestBadRequestStillAbortsTheChain(t *testing.T) {
	t.Parallel()
	if class := ClassifyHTTPStatus(http.StatusBadRequest, `{"error":"unknown model"}`); class != FailurePermanent {
		t.Fatalf("HTTP 400 classified %s, want permanent", class)
	}
	err := &DriverError{Class: FailurePermanent, Err: errors.New("unknown model")}
	if IsRetryableProviderError(t.Context(), err) {
		t.Error("a 400 walked the chain; every provider will reject it identically")
	}
}

// A provider that says when to come back is believed — upward only.
//
// The exponential backoff starts at a few seconds. A provider asking
// for a minute therefore got retried several more times before the
// guess caught up, and every one of those retries counted against the
// limit being waited out. Honouring the header turns that from a
// self-sustaining problem into one wait.
//
// Upward only because the hint must not be able to SHORTEN a cooldown
// that repeated failures have earned: a provider — or a proxy in front
// of it — answering "Retry-After: 0" would otherwise reset the penalty
// on every attempt.
func TestRetryAfterLengthensButNeverShortensACooldown(t *testing.T) {
	t.Parallel()

	// A fixed clock, because the package already provides one and the
	// alternative is asserting a duration against wall time: the
	// wall-clock version passed in isolation and failed once under a
	// loaded full-suite run, which is the least useful way for a test
	// to tell you something.
	frozen := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("a longer hint wins over the computed backoff", func(t *testing.T) {
		h := NewProviderHealth()
		atFixed(h, frozen)
		h.RecordFailureAfter("alibaba", FailureTransient, 90*time.Second)
		if got := h.CooldownRemaining("alibaba"); got != 90*time.Second {
			t.Errorf("cooldown = %v, want exactly the 90s the provider asked for", got)
		}
	})

	t.Run("a shorter hint does not undercut it", func(t *testing.T) {
		h := NewProviderHealth()
		atFixed(h, frozen)
		// Earn a long cooldown the hard way.
		for range 6 {
			h.RecordFailure("alibaba", FailureTransient)
		}
		earned := h.CooldownRemaining("alibaba")
		h.RecordFailureAfter("alibaba", FailureTransient, time.Millisecond)
		if got := h.CooldownRemaining("alibaba"); got < earned {
			t.Errorf("a 1ms hint cut an earned %v cooldown down to %v", earned, got)
		}
	})

	t.Run("no hint behaves exactly as before", func(t *testing.T) {
		a, b := NewProviderHealth(), NewProviderHealth()
		atFixed(a, frozen)
		atFixed(b, frozen)
		a.RecordFailure("x", FailureTransient)
		b.RecordFailureAfter("x", FailureTransient, 0)
		if a.CooldownRemaining("x") != b.CooldownRemaining("x") {
			t.Error("passing no hint changed the existing backoff")
		}
	})
}

// Retry-After arrives in two legal shapes, and a skewed clock must not
// turn a date in the past into "retry immediately" via a negative wait.
func TestParseRetryAfterAcceptsBothFormsAndNeverGoesNegative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"30", 30 * time.Second},
		{"0", 0},
		{"-5", 0},
		{"not-a-number", 0},
		{now.Add(45 * time.Second).Format(http.TimeFormat), 45 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.header != "" {
			h.Set("Retry-After", tc.header)
		}
		if got := parseRetryAfter(h, now); got != tc.want {
			t.Errorf("Retry-After %q -> %v, want %v", tc.header, got, tc.want)
		}
	}
}

// A failure message has to be readable where it is actually read.
//
// "every provider is in cooldown; check the logs for credential or
// quota errors" is the string an inbox item records as its failure and
// the string the console shows. Neither the person reading a queue nor
// the bot that was told its task failed has the node's stderr to hand,
// so that sentence sent them somewhere they could not go — while the
// health tracker already held the answer.
func TestDemotionsAreDescribedWhereTheyWillBeRead(t *testing.T) {
	t.Parallel()

	h := NewProviderHealth()
	h.RecordFailureAfter("alibaba-fast", FailureTransient, 45*time.Second)
	h.RecordFailure("alibaba-pro", FailureCredential)

	got := describeDemotions(h)
	for _, want := range []string{
		"alibaba-fast", "transient failures",
		"alibaba-pro", "credentials rejected",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description is missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "check the logs") {
		t.Errorf("still deferring to the logs: %s", got)
	}

	// Stable ordering: the same outage twice must read as one problem,
	// not two. Map iteration order would otherwise vary per call.
	if second := describeDemotions(h); second != got {
		t.Errorf("description is not stable:\n  %s\n  %s", got, second)
	}
}

// Nothing demoted is a contradiction at this call site, and saying so
// beats an empty clause that reads like a truncated message.
func TestDescribeDemotionsWithNothingDemoted(t *testing.T) {
	t.Parallel()
	if got := describeDemotions(NewProviderHealth()); got == "" {
		t.Error("produced an empty description")
	}
}
