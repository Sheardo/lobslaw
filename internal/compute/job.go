package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Generation is not request/response, and the three vendors surveyed
// share no protocol whatsoever:
//
//	Alibaba Wan  POST + X-DashScope-Async → opaque task_id  → GET /tasks/{id}      → task_status enum
//	Vertex Veo   predictLongRunning       → operation NAME  → fetchPredictOperation → done: true
//	                                                          (a POST, not a GET on the handle)
//	Bedrock      StartAsyncInvoke         → invocationArn   → GetAsyncInvoke        → status field
//
// Not dialects of one pattern — three unrelated designs. So this
// interface commits to none of them: the handle is opaque and
// driver-owned, and polling is a driver METHOD rather than a URL the
// caller builds. Anything that assumes "task id embedded in a path"
// fits exactly one of the three and breaks on the other two.

// JobStatus is the normalised lifecycle. Vendors spell these
// differently (an enum, a done bool, a status string); the driver
// maps its own vocabulary onto these four so the scheduler branches
// on one set.
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// Terminal reports whether the job will never change again. The poll
// loop exists to stop, and this is what stops it.
func (s JobStatus) Terminal() bool {
	return s == JobSucceeded || s == JobFailed
}

// JobHandle identifies an in-flight job.
//
// Serialisable is the load-bearing word. The handle is stored on a
// commitment and polled later — possibly minutes later, possibly on a
// DIFFERENT NODE after a crash takeover. It has to survive a
// round-trip through raft and mean the same thing to a process that
// never saw the submission. That rules out anything holding a live
// connection, a closure or a pointer.
type JobHandle struct {
	// Driver names who can interpret Raw. A handle is meaningless
	// without it: an ARN and an operation resource name are both
	// strings, and only the driver that minted one can poll it.
	Driver string `json:"driver"`

	// Raw is driver-owned and opaque above this line. An ARN, an
	// operation resource name, a bare task id, or a JSON blob if the
	// driver needs more than one field to resume.
	Raw string `json:"raw"`
}

func (h JobHandle) Valid() bool { return h.Driver != "" && h.Raw != "" }

// Encode renders the handle for storage on a commitment's params map.
func (h JobHandle) Encode() (string, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("job handle: encode: %w", err)
	}
	return string(b), nil
}

// DecodeJobHandle is the inverse, used by whichever node picks the
// commitment up.
func DecodeJobHandle(s string) (JobHandle, error) {
	var h JobHandle
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return JobHandle{}, fmt.Errorf("job handle: decode: %w", err)
	}
	if !h.Valid() {
		return JobHandle{}, fmt.Errorf("job handle: incomplete after decode (driver=%q raw=%q)", h.Driver, h.Raw)
	}
	return h, nil
}

// JobRequest is the modality-agnostic submission. The prompt and the
// knobs a generator needs; anything vendor-specific rides in Options
// rather than growing this struct per vendor.
type JobRequest struct {
	Modality Modality
	Model    string
	Prompt   string

	// Options is driver-specific (aspect ratio, duration, seed,
	// negative prompt). Deliberately untyped: every vendor has a
	// different set and typing the union here would make this struct
	// the place every vendor difference leaks into.
	Options map[string]string

	// DestMount, when set, asks the driver to have the provider write
	// directly into an operator-owned bucket. Bedrock REQUIRES this;
	// Veo accepts it; Wan cannot do it. A driver that cannot honour it
	// ignores it and returns a URL or inline bytes instead, and the
	// resolver normalises the difference away.
	DestMount string
}

// JobState is one poll's answer.
type JobState struct {
	Status JobStatus

	// Usage is often only known at completion — a video's cost depends
	// on the seconds actually produced — so it is empty until then.
	Usage ModalUsage

	// Err carries the provider's failure text when Status is
	// JobFailed. Not an error value: this crosses raft as a string.
	Err string

	// Artifact is populated on success. Nil otherwise.
	Artifact *Artifact
}

// JobDriver is a modality whose work outlives a turn.
type JobDriver interface {
	// Submit starts the job and returns a handle that survives storage.
	Submit(ctx context.Context, req JobRequest) (JobHandle, error)

	// Poll reports current state. Called by whichever node holds the
	// commitment claim, which need not be the node that submitted.
	Poll(ctx context.Context, h JobHandle) (JobState, error)

	// PollInterval is the driver's own cadence — roughly 15s for Wan,
	// 10-60s suggested for Veo, against runtimes of 1-5 minutes. The
	// scheduler asks rather than assuming, because a single global
	// interval is either wasteful against one vendor or rate-limited
	// by another.
	PollInterval() time.Duration
}

// MaxJobLifetime bounds a job that never reaches a terminal state.
// Without it a provider that loses a task leaves a commitment being
// polled until the heat death of the cluster.
const MaxJobLifetime = 2 * time.Hour
