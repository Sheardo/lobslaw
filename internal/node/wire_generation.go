package node

import (
	"context"
	"fmt"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/scheduler"
	"github.com/jmylchreest/lobslaw/pkg/config"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// A generation job outlives its turn, so it is a commitment: work to
// finish later, claimed by one node, delivered when done. lobslaw
// already owns that machinery — the revision-guarded claim CAS, the
// TTL, takeover-on-crash — and building a second job store beside it
// would repeat the mistake of building a second approval store beside
// the policy engine.
//
// The commitment needs no new schema. Its params map carries the
// driver-owned handle as a string, which is exactly what JobHandle
// serialises to.

// GenerationPollHandlerRef is the handler every generation commitment
// points at.
const GenerationPollHandlerRef = "generation:poll"

// Commitment param keys. The handle is the only one that must survive
// verbatim; the rest are context for delivery and for giving up.
const (
	paramJobHandle    = "job_handle"
	paramJobDeadline  = "job_deadline"
	paramArtifactName = "artifact_name"
	paramOrigChannel  = "originator_channel"
	paramOrigChatID   = "originator_chat_id"
)

// runGenerationPoll advances one generation job by exactly one poll.
//
// It is registered Idempotent(), so the scheduler runs it BEFORE
// closing the commitment. That matters: this handler is not finished
// when it returns, it is finished when the JOB is, and a crash
// mid-poll must leave the commitment pending so another node can take
// over. The alternative loses work that is already running and
// already being billed.
func (n *Node) runGenerationPoll(ctx context.Context, c *lobslawv1.AgentCommitment) error {
	raw := c.Params[paramJobHandle]
	if raw == "" {
		return fmt.Errorf("generation: commitment %q carries no job handle", c.Id)
	}
	h, err := compute.DecodeJobHandle(raw)
	if err != nil {
		// Unrecoverable: nothing can ever poll this job again. Closing
		// the commitment is the honest outcome — leaving it pending
		// would re-poll a handle that will never decode.
		return fmt.Errorf("generation: commitment %q: %w", c.Id, err)
	}

	driver, ok := n.jobDriver(h.Driver)
	if !ok {
		return fmt.Errorf("generation: no driver %q registered for commitment %q "+
			"(a job was submitted by a driver this node does not have)", h.Driver, c.Id)
	}

	// Give up eventually. A provider that loses a task would otherwise
	// leave this commitment being polled forever.
	if deadline := parseDeadline(c.Params[paramJobDeadline]); !deadline.IsZero() && time.Now().After(deadline) {
		n.notifyGeneration(ctx, c, fmt.Sprintf(
			"The generation job gave up: it did not finish within %s.", compute.MaxJobLifetime))
		return fmt.Errorf("generation: commitment %q exceeded %s", c.Id, compute.MaxJobLifetime)
	}

	st, err := driver.Poll(ctx, h)
	if err != nil {
		// A poll that fails is not a job that failed. The distinction
		// is the failure class: a 503 from the status endpoint says
		// nothing about the job, whereas a 400 says this handle will
		// never be pollable.
		if compute.ClassifyFailure(err) == compute.FailurePermanent {
			return fmt.Errorf("generation: commitment %q: polling is permanently broken: %w", c.Id, err)
		}
		return scheduler.RetryAfterIn(driver.PollInterval(),
			fmt.Sprintf("poll failed transiently: %v", err))
	}

	switch st.Status {
	case compute.JobPending, compute.JobRunning:
		return scheduler.RetryAfterIn(driver.PollInterval(), "job still running")

	case compute.JobFailed:
		n.log.Warn("generation: job failed",
			"commitment", c.Id, "driver", h.Driver, "err", st.Err)
		n.notifyGeneration(ctx, c, "The generation job failed: "+st.Err)
		return nil

	case compute.JobSucceeded:
		return n.deliverGeneration(ctx, c, st)

	default:
		return fmt.Errorf("generation: commitment %q: driver %q reported unknown status %q",
			c.Id, h.Driver, st.Status)
	}
}

// deliverGeneration resolves the artifact and tells the user where it
// landed.
//
// The resolve step is where an expiring vendor URL becomes a file in
// operator storage, and it is deliberately the LAST thing that can
// fail: by this point the job has succeeded and been billed, so a
// failure here is worth reporting loudly rather than retrying
// silently past the URL's expiry.
func (n *Node) deliverGeneration(ctx context.Context, c *lobslawv1.AgentCommitment, st compute.JobState) error {
	if st.Artifact == nil {
		return fmt.Errorf("generation: commitment %q succeeded with no artifact", c.Id)
	}
	resolver := n.artifactResolver()
	if resolver == nil {
		return fmt.Errorf("generation: commitment %q: no artifact resolver wired; "+
			"the job succeeded and the result cannot be stored", c.Id)
	}

	name := c.Params[paramArtifactName]
	if name == "" {
		name = c.Id
	}
	got, err := resolver.Resolve(ctx, st.Artifact, name)
	if err != nil {
		n.notifyGeneration(ctx, c, "The generation finished but the result could not be saved: "+err.Error())
		return fmt.Errorf("generation: commitment %q: resolve artifact: %w", c.Id, err)
	}

	n.log.Info("generation: job delivered",
		"commitment", c.Id, "mount", got.Mount, "path", got.Path,
		"bytes", got.Bytes, "unit", st.Usage.Unit, "quantity", st.Usage.Quantity)

	n.notifyGeneration(ctx, c, fmt.Sprintf("Your generation is ready: %s (in %s).", got.Path, got.Mount))
	return nil
}

// notifyGeneration is best-effort. A delivery failure must not fail
// the handler: the artifact is already saved, and returning an error
// would only re-run a poll that has nothing left to do.
func (n *Node) notifyGeneration(ctx context.Context, c *lobslawv1.AgentCommitment, body string) {
	ch, id := c.Params[paramOrigChannel], c.Params[paramOrigChatID]
	if ch == "" || id == "" {
		n.log.Info("generation: no originating channel recorded; result not delivered",
			"commitment", c.Id, "body", body)
		return
	}
	notifier := &researchNotifyAdapter{tg: n.telegramHandler, log: n.log}
	if err := notifier.Notify(ctx, ch, id, body); err != nil {
		n.log.Warn("generation: notify failed", "commitment", c.Id, "err", err)
	}
}

// jobDriver looks up the driver that minted a handle. A handle is
// meaningless without it — an ARN and an operation resource name are
// both strings, and only the driver that created one can poll it.
func (n *Node) jobDriver(name string) (compute.JobDriver, bool) {
	n.jobDriverMu.RLock()
	defer n.jobDriverMu.RUnlock()
	d, ok := n.jobDrivers[name]
	return d, ok
}

// RegisterJobDriver installs a generation driver under its name. The
// name is what ends up inside every JobHandle the driver mints, so it
// must be stable across restarts: a handle stored before a rename can
// never be polled again.
func (n *Node) RegisterJobDriver(name string, d compute.JobDriver) {
	n.jobDriverMu.Lock()
	defer n.jobDriverMu.Unlock()
	if n.jobDrivers == nil {
		n.jobDrivers = map[string]compute.JobDriver{}
	}
	n.jobDrivers[name] = d
}

// nodeMounts adapts the operator's declared storage mounts to the
// resolver's seam. Reading config rather than the storage service
// keeps this to the mounts an operator actually declared, and keeps
// compute free of a dependency on the storage package.
type nodeMounts struct{ mounts []config.StorageMountConfig }

func (m nodeMounts) MountRoot(label string) (string, bool) {
	for _, mt := range m.mounts {
		if mt.Label != label {
			continue
		}
		// A read-only mount is not somewhere to land a generated file.
		if mt.Mode != "" && mt.Mode != "rw" {
			return "", false
		}
		return mt.Path, true
	}
	return "", false
}

// artifactResolver builds the resolver that turns whichever shape a
// vendor returned into a path inside a mount. Returns nil when there
// is nowhere to write, which the caller reports rather than silently
// dropping a job that has already been paid for.
func (n *Node) artifactResolver() *compute.ArtifactResolver {
	mounts := n.cfg.Storage.Mounts
	dest := n.cfg.Compute.ArtifactMount

	if dest != "" {
		// An operator who named a mount meant that mount. Falling back
		// to a different one would put generated files somewhere they
		// did not choose — possibly a different backend, with different
		// retention and different people able to read it.
		if _, ok := (nodeMounts{mounts: mounts}).MountRoot(dest); !ok {
			n.log.Error("compute: artifact_mount names a mount that is missing or read-only; "+
				"generation results cannot be stored", "mount", dest)
			return nil
		}
	} else {
		for _, mt := range mounts {
			if mt.Mode == "" || mt.Mode == "rw" {
				dest = mt.Label
				break
			}
		}
		if dest != "" && len(mounts) > 1 {
			n.log.Warn("compute: no artifact_mount configured; generated files will land in the "+
				"first writable mount — set compute.artifact_mount to be explicit",
				"chose", dest, "mounts", len(mounts))
		}
	}
	if dest == "" {
		return nil
	}
	return &compute.ArtifactResolver{
		Mounts:       nodeMounts{mounts: mounts},
		DefaultMount: dest,
	}
}

func parseDeadline(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// NewGenerationCommitment builds the commitment for a freshly
// submitted job. Separated from the handler so the submit path and
// the poll path agree on the param names by construction rather than
// by two copies of the same string literals.
func NewGenerationCommitment(id string, h compute.JobHandle, iv time.Duration, owner, channel, chatID, name string) (*lobslawv1.AgentCommitment, error) {
	raw, err := h.Encode()
	if err != nil {
		return nil, err
	}
	return &lobslawv1.AgentCommitment{
		Id:         id,
		HandlerRef: GenerationPollHandlerRef,
		Status:     "pending",
		Owner:      owner,
		Trigger:    "time",
		Reason:     "generation job " + h.Driver,
		Params: map[string]string{
			paramJobHandle:    raw,
			paramJobDeadline:  time.Now().Add(compute.MaxJobLifetime).Format(time.RFC3339),
			paramArtifactName: name,
			paramOrigChannel:  channel,
			paramOrigChatID:   chatID,
		},
	}, nil
}

// resolveSpeakEndpoints finds the TTS provider chain: an explicit
// [compute.speak] override, else every provider tagged "speak" in
// priority order.
func (n *Node) resolveSpeakEndpoints() []*llmEndpoint {
	return n.resolveModalityEndpoints("speak", n.cfg.Compute.Speak.Provider, compute.CapabilitySpeak)
}

// wireSpeakTools registers the speak (text-to-speech) builtin.
//
// Registered only when there is somewhere to put the audio. A speak
// tool with no writable mount would take the model's text, bill the
// provider for synthesis, and then have nowhere to land the result —
// so the honest behaviour is to not offer the tool and let the agent
// say it cannot do it.
func (n *Node) wireSpeakTools(builtins *compute.Builtins) error {
	eps := n.resolveSpeakEndpoints()
	if len(eps) == 0 {
		return nil
	}
	resolver := n.artifactResolver()
	if resolver == nil {
		n.log.Warn("compute: speak provider configured but no writable artifact mount; " +
			"skipping the speak tool")
		return nil
	}

	cfgs := make([]compute.SpeakConfig, 0, len(eps))
	for _, ep := range eps {
		d, err := compute.NewOpenAISpeakDriver(compute.OpenAISpeakConfig{
			Endpoint:   ep.endpoint,
			Model:      ep.model,
			Credential: compute.NewBearerCredential(ep.apiKey),
		})
		if err != nil {
			n.log.Warn("compute: speak provider skipped", "via", ep.via, "err", err)
			continue
		}
		cfgs = append(cfgs, compute.SpeakConfig{Driver: d, Resolver: resolver})
	}
	if len(cfgs) == 0 {
		return nil
	}

	if err := compute.RegisterSpeakBuiltin(builtins, cfgs...); err != nil {
		return fmt.Errorf("register speak: %w", err)
	}
	if err := n.toolRegistry.Register(compute.SpeakToolDef()); err != nil {
		return fmt.Errorf("register speak tool def: %w", err)
	}
	n.log.Debug("compute: speak registered",
		"model", eps[0].model, "via", eps[0].via, "chain_len", len(cfgs))
	return nil
}
