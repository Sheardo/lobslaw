// Package compute_test rather than compute: drivertest imports
// compute, so an in-package test importing it is a cycle.
package compute_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivertest"
)

// The mock is the first subject through the async suite, and until a
// real generator lands it is the only proof the contract is
// satisfiable at all. A contract nothing passes is a wish.
func TestMockJobDriverConformance(t *testing.T) {
	t.Parallel()
	drivertest.RunJob(t, drivertest.JobSubject{
		Name: "mock-job",
		Job:  &compute.MockJobDriver{PollsBeforeDone: 2},
	})
}

// Each delivery mode must satisfy the same contract; the suite must
// not have been written around whichever one was implemented first.
func TestMockJobDriverConformanceAcrossDeliveryModes(t *testing.T) {
	t.Parallel()
	for _, kind := range []compute.ArtifactKind{
		compute.ArtifactInline, compute.ArtifactURL, compute.ArtifactMount,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			drivertest.RunJob(t, drivertest.JobSubject{
				Name: "mock-job-" + string(kind),
				Job:  &compute.MockJobDriver{PollsBeforeDone: 1, Kind: kind},
			})
		})
	}
}

// A failed job must carry text an operator can act on. Silence here
// means a commitment closes with no explanation of what was billed.
func TestFailedJobCarriesReason(t *testing.T) {
	t.Parallel()
	d := &compute.MockJobDriver{FailWith: "content policy: prompt rejected"}
	h, err := d.Submit(context.Background(), compute.JobRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := d.Poll(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != compute.JobFailed {
		t.Fatalf("status = %q, want failed", st.Status)
	}
	if !strings.Contains(st.Err, "content policy") {
		t.Errorf("Err = %q, want the provider's reason", st.Err)
	}
	if st.Artifact != nil {
		t.Error("a failed job produced an artifact")
	}
}

// A handle only means something to the driver that minted it. Polling
// one driver with another's handle is a wiring bug, and it must fail
// loudly rather than being interpreted as a job that does not exist.
func TestPollRejectsForeignHandle(t *testing.T) {
	t.Parallel()
	d := &compute.MockJobDriver{}
	_, err := d.Poll(context.Background(), compute.JobHandle{Driver: "veo", Raw: "projects/x/operations/y"})
	if err == nil {
		t.Fatal("polled a handle belonging to another driver")
	}
	if !strings.Contains(err.Error(), "veo") {
		t.Errorf("error should name the foreign driver, got: %v", err)
	}
}

// The handle crosses raft as a string and comes back on a node that
// never saw the submission. Everything about it must survive that.
func TestJobHandleEncoding(t *testing.T) {
	t.Parallel()

	t.Run("round trips", func(t *testing.T) {
		t.Parallel()
		// A Vertex operation name — slashes and all — is the shape most
		// likely to break a naive encoding.
		in := compute.JobHandle{Driver: "veo", Raw: "projects/p/locations/us/operations/12345"}
		enc, err := in.Encode()
		if err != nil {
			t.Fatal(err)
		}
		out, err := compute.DecodeJobHandle(enc)
		if err != nil {
			t.Fatal(err)
		}
		if out != in {
			t.Errorf("round trip changed the handle: %+v → %+v", in, out)
		}
	})

	t.Run("rejects incomplete", func(t *testing.T) {
		t.Parallel()
		for _, s := range []string{`{}`, `{"driver":"veo"}`, `{"raw":"abc"}`, `not json`} {
			if _, err := compute.DecodeJobHandle(s); err == nil {
				t.Errorf("decoded %q into a usable handle", s)
			}
		}
	})
}

// The scheduler asks the driver for its cadence because the vendors
// differ by 4x. A shared constant would be wasteful against one and
// rate-limited by another.
func TestPollIntervalIsDriverOwned(t *testing.T) {
	t.Parallel()
	slow := &compute.MockJobDriver{Interval: 30 * time.Second}
	fast := &compute.MockJobDriver{Interval: 5 * time.Second}
	if slow.PollInterval() == fast.PollInterval() {
		t.Error("two drivers reported the same interval; the cadence is not actually driver-owned")
	}
}
