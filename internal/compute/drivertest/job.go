package drivertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// RunJob is the conformance suite for asynchronous modalities.
//
// It asserts the properties the SCHEDULER depends on, not what the
// provider generates. The scheduler stores a handle on a commitment,
// may be a different process by the time it polls, and must eventually
// stop. Every case below is one of those three concerns.
func RunJob(t *testing.T, s JobSubject) {
	t.Helper()
	if s.Job == nil {
		t.Fatalf("%s: no job driver — nothing to conform to", s.Name)
	}
	t.Run(s.Name+"/job", func(t *testing.T) {
		t.Run("handle survives storage", func(t *testing.T) { jobHandleRoundTrip(t, s) })
		t.Run("polls to a terminal state", func(t *testing.T) { jobReachesTerminal(t, s) })
		t.Run("declares a sane poll interval", func(t *testing.T) { jobPollInterval(t, s) })
		t.Run("honours cancellation", func(t *testing.T) { jobCancel(t, s) })
	})
}

// JobSubject is an async driver under test.
type JobSubject struct {
	Name string
	Job  compute.JobDriver

	// Request is what to submit. Zero value gets a plain prompt.
	Request compute.JobRequest

	// Live marks a real endpoint, which relaxes the polling bound
	// because a real generator takes minutes rather than milliseconds.
	Live bool
}

func (s JobSubject) request() compute.JobRequest {
	r := s.Request
	if r.Prompt == "" {
		r.Prompt = "a red cube rotating"
	}
	if r.Modality == "" {
		r.Modality = compute.ModalityVideo
	}
	return r
}

// The handle is stored on a commitment and polled later, possibly by
// a different node after a crash takeover. A driver whose handle does
// not survive that round-trip loses the job: the work is still running
// and being billed, and nothing can ever collect it.
func jobHandleRoundTrip(t *testing.T, s JobSubject) {
	h, err := s.Job.Submit(context.Background(), s.request())
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if !h.Valid() {
		t.Fatalf("submit returned an incomplete handle: driver=%q raw=%q", h.Driver, h.Raw)
	}

	enc, err := h.Encode()
	if err != nil {
		t.Fatalf("handle does not encode: %v", err)
	}
	back, err := DecodeJobHandleForTest(enc)
	if err != nil {
		t.Fatalf("handle does not survive a storage round trip: %v", err)
	}
	if back != h {
		t.Errorf("handle changed across the round trip:\n  before %+v\n  after  %+v", h, back)
	}

	// The decoded handle — not the original — must be pollable. That is
	// the case that actually happens after a takeover.
	if _, err := s.Job.Poll(context.Background(), back); err != nil {
		t.Errorf("a handle that came back from storage could not be polled: %v", err)
	}
}

// The poll loop exists to stop. A driver that never reports a terminal
// status leaves a commitment being polled forever.
func jobReachesTerminal(t *testing.T, s JobSubject) {
	h, err := s.Job.Submit(context.Background(), s.request())
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	if s.Live {
		deadline = time.Now().Add(LiveTimeout)
	}
	var st compute.JobState
	for time.Now().Before(deadline) {
		st, err = s.Job.Poll(context.Background(), h)
		if err != nil {
			t.Fatalf("poll failed: %v", err)
		}
		if st.Status.Terminal() {
			break
		}
		if s.Live {
			time.Sleep(s.Job.PollInterval())
		}
	}
	if !st.Status.Terminal() {
		t.Fatalf("never reached a terminal state (last status %q)", st.Status)
	}

	if st.Status == compute.JobSucceeded {
		if st.Artifact == nil {
			t.Error("succeeded without an artifact; the whole point of the job is the output")
		} else if err := artifactLooksUsable(st.Artifact); err != nil {
			t.Errorf("succeeded with an unusable artifact: %v", err)
		}
	}
	if st.Status == compute.JobFailed && st.Err == "" {
		t.Error("failed with no error text; the operator has nothing to act on")
	}
}

func artifactLooksUsable(a *compute.Artifact) error {
	switch a.Kind {
	case compute.ArtifactURL:
		if a.URL == "" {
			return errors.New("kind=url but no URL")
		}
	case compute.ArtifactInline:
		if len(a.Bytes) == 0 {
			return errors.New("kind=inline but no bytes")
		}
	case compute.ArtifactMount:
		if a.Mount == "" || a.Path == "" {
			return errors.New("kind=mount but no mount/path")
		}
	default:
		return errors.New("unknown artifact kind " + string(a.Kind))
	}
	return nil
}

// A zero interval spins the scheduler; an hour-long one makes a
// two-minute job take an hour to notice. The driver owns the cadence
// because the vendors differ, but it still has to be a cadence.
func jobPollInterval(t *testing.T, s JobSubject) {
	iv := s.Job.PollInterval()
	if iv <= 0 {
		t.Fatalf("poll interval %v would spin the scheduler", iv)
	}
	if iv > 10*time.Minute {
		t.Errorf("poll interval %v is longer than most jobs take to run", iv)
	}
}

// A cancelled turn must not leave a poll in flight.
func jobCancel(t *testing.T, s JobSubject) {
	h, err := s.Job.Submit(context.Background(), s.request())
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Job.Poll(ctx, h); err == nil {
		t.Error("poll ignored a cancelled context")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want something wrapping context.Canceled", err)
	}
}

// DecodeJobHandleForTest exists so the suite exercises the same
// decoder production uses rather than a test-local copy that could
// drift from it.
func DecodeJobHandleForTest(s string) (compute.JobHandle, error) {
	return compute.DecodeJobHandle(s)
}
