package compute

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MockJobDriver is a generation driver that touches no network.
//
// It exists for the same reason MockChatFactory does: the async path
// has more moving parts than the synchronous one — submit, store,
// takeover, poll, fetch, resolve — and every one of them should be
// exercisable in CI, on a plane, and without a vendor account. It is a
// registered driver rather than a test-only injection so the config
// path is exercised too.
//
// It also encodes the awkward parts of the real vendors deliberately:
// the handle is opaque (a driver-owned string, not a URL), the job
// takes several polls rather than succeeding immediately, and usage is
// only reported at completion.
type MockJobDriver struct {
	// PollsBeforeDone is how many polls report running before success.
	// Zero means succeed on the first poll.
	PollsBeforeDone int

	// FailWith, when non-empty, makes the job reach JobFailed with
	// this text instead of succeeding.
	FailWith string

	// Kind selects which of the three delivery modes to imitate.
	// Empty means inline, the easiest to assert on.
	Kind ArtifactKind

	// Interval is the declared cadence. Zero picks something small so
	// tests do not sleep.
	Interval time.Duration

	mu    sync.Mutex
	polls map[string]int
	seq   int
}

const mockJobDriverName = "mock-job"

// Submit mints an opaque handle. The format is deliberately NOT a URL
// or anything the caller could parse and act on — a caller that starts
// interpreting Raw is a caller that will break against the real
// vendors, all three of which use a different shape.
func (m *MockJobDriver) Submit(ctx context.Context, req JobRequest) (JobHandle, error) {
	if err := ctx.Err(); err != nil {
		return JobHandle{}, Permanent(fmt.Errorf("mock job: %w", err))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	// Modality rides inside the opaque blob to prove the driver can
	// carry its own state across a raft round-trip without the schema
	// above it knowing anything about the contents.
	raw := fmt.Sprintf("job-%d|%s|%s", m.seq, req.Modality, req.DestMount)
	return JobHandle{Driver: mockJobDriverName, Raw: raw}, nil
}

func (m *MockJobDriver) Poll(ctx context.Context, h JobHandle) (JobState, error) {
	if err := ctx.Err(); err != nil {
		return JobState{}, Permanent(fmt.Errorf("mock job: %w", err))
	}
	if h.Driver != mockJobDriverName {
		return JobState{}, Permanent(fmt.Errorf(
			"mock job: handle belongs to driver %q, not %q", h.Driver, mockJobDriverName))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.polls == nil {
		m.polls = map[string]int{}
	}
	m.polls[h.Raw]++
	n := m.polls[h.Raw]

	if n <= m.PollsBeforeDone {
		return JobState{Status: JobRunning}, nil
	}
	if m.FailWith != "" {
		return JobState{Status: JobFailed, Err: m.FailWith}, nil
	}

	// Usage lands only at completion, as it does for a real generator:
	// the cost of a video depends on the seconds actually produced.
	return JobState{
		Status:   JobSucceeded,
		Usage:    ModalUsage{Unit: UnitVideoSeconds, Quantity: 4, BilledTo: BillingBalance, CostUSD: 0},
		Artifact: m.artifact(h),
	}, nil
}

func (m *MockJobDriver) artifact(h JobHandle) *Artifact {
	destMount := ""
	if parts := strings.Split(h.Raw, "|"); len(parts) == 3 {
		destMount = parts[2]
	}
	switch m.Kind {
	case ArtifactURL:
		return &Artifact{
			Kind: ArtifactURL, URL: "https://example.invalid/generated.mp4",
			ExpiresAt: time.Now().Add(24 * time.Hour), MIME: "video/mp4",
		}
	case ArtifactMount:
		mount := destMount
		if mount == "" {
			mount = "store"
		}
		return &Artifact{
			Kind: ArtifactMount, Mount: mount,
			Path: "generated/mock-" + strconv.Itoa(m.polls[h.Raw]) + ".mp4", MIME: "video/mp4",
		}
	default:
		return &Artifact{Kind: ArtifactInline, Bytes: []byte("MOCKVIDEO"), MIME: "video/mp4"}
	}
}

func (m *MockJobDriver) PollInterval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return 10 * time.Millisecond
}
