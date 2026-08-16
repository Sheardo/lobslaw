# lobslaw — Providers, drivers and modalities

Design for the provider layer: how lobslaw reaches an LLM, a vision
model, a transcriber, a speech synthesiser, an image or video
generator — and how an operator adds one lobslaw has never heard of.

This is a **design document**, not a description of what ships today.
The inventory below says which parts already exist, because a
surprising amount does and the work is mostly unification rather than
invention.

---

## What exists today

| Concern | State |
|---|---|
| Chat | `LLMClient`, one OpenAI-compatible HTTP driver. `LLMProvider` is a one-method interface implemented by it and `MockProvider` |
| Provider registry | `ProviderRegistry`: label → endpoint/model/key/pricing/trust-tier, plus `Chain()` walking `Backup` pointers. Cycles rejected at config load |
| Capabilities | `CapabilityChat`, `-Vision`, `-AudioTranscribe`, `-AudioMultimodal`, `-PDF`, `-Embeddings`. Declared in config, and optionally auto-discovered from models.dev with a 24h disk cache |
| Selection | `SelectByCapability(providers, anyOf...)` returns matches ordered by priority |
| Vision / audio / PDF | `read_image`, `read_audio`, `read_pdf` builtins, each with its own config struct, its own endpoint and key, and its own wire-format enum |
| Embeddings | `EmbeddingClient` with a third format enum |
| Generation | **Nothing.** No image, video or speech synthesis |

Two observations drive everything below.

**The driver concept already exists three times, under three names.**
`VisionFormat` (openai/anthropic/gemini), `AudioFormat`
(openai/openrouter), `EmbeddingFormat` (openai/minimax), plus
`ProviderConfig.Format`. Four parallel spellings of "which wire
protocol does this endpoint speak".

**Per-modality failover is designed for and not built.** From
`SelectByCapability`'s own doc comment: *"the returned slice is kept
ordered for the future fallback-chain layer that will try each in turn
on transient failures."* Chat has failover via `Backup`; vision, audio,
PDF and embeddings have none — a single endpoint, and if it is down the
capability is simply gone.

---

## The distinction the code is missing

**A driver is a wire protocol.** Compiled in, one per request/response
shape: `openai`, `anthropic`, `gemini`. There are few of them and they
change rarely.

**A provider is a configured instance**: label + modality + driver +
endpoint + model + credential + optional backup. There are many, and
operators add them constantly.

Naming this resolves how to support "custom variants" **for chat**,
where OpenAI compatibility is close to universal: a vendor offering an
OpenAI-shaped chat endpoint is a *provider* using the `openai` driver
at a different endpoint with a different model name. That is already
how `LLMClient` works.

**It does not generalise to the generation modalities**, and assuming
it does is the trap. There is no de-facto standard for image or video
generation: the same vendor's image and video APIs differ from each
other, billing units differ per model, and the whole interaction shape
differs (see [Generation is not request/response](#generation-is-not-requestresponse)).
For those, a new provider often really does need a new driver — which
is precisely why external drivers must be easy to write.

The set of drivers lobslaw must actually implement is therefore small:
the three or four real protocol families, plus `mock` and `external`.

---

## Modalities

```
chat         text + tools    → text | tool-calls
embedding    text            → vector
vision       image + prompt  → text
transcribe   audio           → text                 (STT)
document     pdf             → text
speak        text            → audio                (TTS)
image        prompt          → image
video        prompt          → video
```

They split into two families, and the split is the important part.

**Infrastructure modalities — `chat` and `embedding`.** Called directly
by the agent loop and by memory. Not visible to the model.

**Tool modalities — everything else.** Surfaced to the model as
ordinary tools: `read_image`, `read_audio`, `read_pdf`, `speak`,
`generate_image`, `generate_video`.

The second is the existing pattern and it should be kept, because it
earns four properties for free:

- **Any text model works.** `Message.Content` stays a plain string; no
  multimodal content parts, no per-provider content encoding in the
  agent loop. The model that decides is not the model that sees.
- **Policy and confirmation already gate them.** `generate_video` can
  carry `effect = "require_confirmation"` on day one — which is what
  you want for something that costs real money per call — using the
  machinery that already exists.
- **Budget already counts them.** They are tool calls.
- **Honest degradation is already there.** `decorateWithAttachments`
  tells the model to say plainly that no vision tool is wired rather
  than pretending to see. A generation tool that is not configured
  simply is not in the tool list.

The alternative — teaching the chat driver about image content parts
per provider — pushes vendor differences into the hottest path in the
system and makes the agent loop's behaviour depend on which model is
selected. Rejected.

---

## Generation is not request/response

The section above treats every modality as one shape: send a request,
get a result. That is true for chat, embedding, vision, transcribe,
document and speak. **It is false for image and video**, and the
difference is structural rather than dialectal.

Alibaba's Wan text-to-video is representative:

```
POST /api/v1/services/aigc/video-generation/video-synthesis
     X-DashScope-Async: enable
  → { task_id, task_status }

GET  /api/v1/tasks/{task_id}          # poll, ~15s interval
  → PENDING → RUNNING → SUCCEEDED | FAILED
  → { video_url }                     # expires 24h
```

Image-to-video takes **1–5 minutes**. The task id is valid for 24
hours; the result URL expires in 24 hours.

Three consequences the request/response shape cannot absorb:

**A generation driver needs a different interface.** Submit, poll,
fetch — plus artifact retrieval, because the result is a URL with an
expiry rather than bytes in the response. A driver interface shaped
like `Chat` cannot express it.

**A turn cannot wait.** A 1–5 minute job blocks the conversation, the
cluster lease heartbeat, and the responsiveness hard timeout (90s) all
at once. Generation must be *deferrable*: the tool call submits, the
turn returns "started", and the result arrives later.

**lobslaw already owns the machinery for that.** `AgentCommitment` plus
the scheduler is exactly "work to finish later, claimed by one node,
delivered when done" — with the revision-guarded claim CAS, the TTL,
the takeover-on-crash and the notify path already built and tested. A
video job is a commitment with a poll handler. Building a second job
store beside it would be the same mistake as building a second
permanent-approval store beside the policy engine.

So: **generation modalities submit a job and register a commitment.**
The scheduler polls; on success it downloads the artifact before the
URL expires and notifies the channel. Nothing new is invented, and
crash recovery, leader-pinning and at-most-once delivery come for free.

Not every generator is async — OpenAI's image API returns the image in
the response. The driver declares which shape it is, and the
synchronous ones skip the commitment path when the expected latency is
small enough to sit inside a turn.

### There is no common async shape either

Three vendors, three unrelated protocols. Not dialects of one pattern:

| | Submit | Handle | Poll | Done signal |
|---|---|---|---|---|
| Alibaba Wan | `POST …/video-synthesis` + `X-DashScope-Async` | `task_id`, opaque string | `GET /api/v1/tasks/{id}` | `task_status` enum |
| Vertex Veo | `predictLongRunning` | operation **resource name**, `projects/…/operations/…` | `fetchPredictOperation` — a POST, not a GET on the handle | `done: true` |
| Bedrock | `StartAsyncInvoke` | `invocationArn` | `GetAsyncInvoke` / `ListAsyncInvokes` | status field |

So the driver interface must treat the job handle as **an opaque
driver-owned value** and polling as **a driver method**, not as "GET
the task URL". Anything that assumes a task id embedded in a path
fits exactly one of the three.

Polling intervals differ too — ~15s for Wan, 10–60s suggested for Veo,
with Veo running 2–5 minutes and Wan 1–5 — so the interval belongs to
the driver rather than to the scheduler.

### Artifacts arrive three different ways

| Mode | Provider | Consequence |
|---|---|---|
| Vendor URL, expiring | Wan — `video_url`, 24h | Must be downloaded promptly; a job whose delivery is delayed past the expiry is lost |
| Inline bytes | Veo without `storageUri` — base64 in the response | Simple, but the whole artifact crosses the response |
| Operator-owned bucket | Bedrock — `outputDataConfig.s3OutputDataConfig.s3Uri`, **mandatory**; Veo with `storageUri` — optional | The provider writes into *your* storage, and needs IAM to do it |

The third mode is a good fit rather than a nuisance: lobslaw already
has a storage layer with local, S3, MinIO and R2 backends and
operator-declared mounts. A generation provider writing into a
lobslaw storage mount is the artifact landing exactly where the agent
can already read it, with no download step and no expiry.

The expiring-URL mode is the one with a deadline attached, and it is
the argument for the commitment path being reliable rather than
best-effort: a poll handler that does not run for 24 hours loses the
output the operator has already paid for.

### Credentials are not always a static key

The dimension that breaks the config shape hardest, and the one I had
not considered at all:

| Provider | Credential |
|---|---|
| OpenAI, Anthropic, DashScope | Static API key, sent as a bearer token |
| Vertex AI | **No static key exists.** The API rejects them outright — *"API keys are not supported by this API"*. Requires a short-lived OAuth2 access token (~1h) minted from a service-account JSON or ADC, and refreshed |
| Bedrock | SigV4 **request signing** with access key, secret and optional session token — a per-request signature, not a header value |

`api_key = "env:OPENAI_KEY"` cannot express either of the last two.
lobslaw's `ResolveSecret` (`env:` / `file:` / `kms:`) resolves a static
value; Vertex needs a token *minter* with refresh, and Bedrock needs a
request *signer*.

So a provider declares a credential **kind**, not a key:

```toml
[[provider]]
label       = "veo"
modality    = "video"
driver      = "vertex"
credential  = { kind = "gcp-service-account", ref = "file:/etc/lobslaw/sa.json" }

[[provider]]
label       = "nova-reel"
modality    = "video"
driver      = "bedrock"
credential  = { kind = "aws-sigv4", region = "us-east-1" }
```

`kind = "static"` with a secret ref stays the default and covers every
provider that works today.

There is prior art in-tree: `CredentialService.IssueForSkillByManifest`
already mints and refreshes short-lived OAuth tokens near expiry for
skills. Provider credentials want the same treatment, not a second
refresh loop.

---

## Billing is not tokens

The second thing "same driver, different endpoint" hides. Generation
providers bill in units that `Usage{PromptTokens, CompletionTokens,
TotalTokens, CachedTokens}` cannot express:

| Unit | Example |
|---|---|
| per token | OpenAI `gpt-image-1` — the image is encoded as tokens |
| per image | most flat-rate image APIs |
| per megapixel | resolution-scaled: 4K costs multiples of 1024² on the same model |
| per second of video | Wan — *"billed per successfully generated second of video"* |
| per second of GPU | Replicate's default for non-official models |
| credits | Stability, Ideogram, and Alibaba's Token Plan |

Today `CostRecord` multiplies token counts by a per-token price. For
every row except the first that yields **zero**, and a cost report
saying a video cost nothing is worse than one that says nothing at all:
it is a confidently wrong answer, and the operator has no reason to
doubt it.

`Usage` therefore needs a unit and a quantity, with token detail as one
case rather than the only case. Pricing follows: a provider declares
what it charges per unit of its own unit.

### Plans change the failure mode, not just the arithmetic

Alibaba's Token Plan bills in **Credits** against a monthly per-seat
quota that does not carry over, and — the important part — **when the
quota is exhausted, API calls are blocked. No pay-as-you-go charge
applies.**

That is a third failure category, and the failover rules above have
only two. Quota exhaustion is not transient (retrying the same provider
will fail identically until the month rolls over) and not a request bug
(the request is fine). It should **fall through to the backup**, which
is typically a pay-as-you-go provider — and it must be surfaced loudly,
because an operator whose plan ran out on the 3rd and who has been
silently billed per-call ever since will want to have known.

So the failover taxonomy is:

| Class | Example | Action |
|---|---|---|
| Transient | 5xx, timeout, rate limit | Fall through, retry-able later |
| Quota exhausted | plan quota spent | Fall through, **warn loudly**, do not retry this provider until reset |
| Permanent | 400, bad model name | Fail the call — the backup will fail identically |

---

## The interface

Three rounds of research changed this design three times, and each time
the thing that moved was one of four axes: **interaction shape**,
**billing unit**, **credential kind**, **artifact delivery**. They will
keep moving. The abstraction's job is to make those the *only* things a
driver states, and to make everything above the driver blind to them.

So the waist is deliberately narrow.

### Synchronous modalities

One method each. A driver implements only the modalities it serves.

```go
type ChatDriver interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

type EmbedDriver interface {
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}

type VisionDriver interface {
	Describe(ctx context.Context, req VisionRequest) (*TextResponse, error)
}

type TranscribeDriver interface {
	Transcribe(ctx context.Context, req AudioRequest) (*TextResponse, error)
}

type SpeakDriver interface {
	Speak(ctx context.Context, req SpeakRequest) (*Artifact, error)
}
```

### Asynchronous modalities

The three vendors surveyed share no protocol, so the interface commits
to none. `JobHandle` is **opaque and serialisable** — the driver's own
encoding of an ARN, an operation resource name or a bare task id — and
polling is a driver method rather than a URL the caller constructs.

Serialisable is the load-bearing word: the handle is stored on a
commitment and polled later, possibly on a different node after a
crash takeover. It must survive a round-trip through raft and mean the
same thing to a different process.

```go
type JobDriver interface {
	Submit(ctx context.Context, req JobRequest) (JobHandle, error)
	Poll(ctx context.Context, h JobHandle) (JobState, error)
	Fetch(ctx context.Context, st JobState) (*Artifact, error)

	// PollInterval is the driver's own cadence — ~15s for Wan,
	// 10-60s for Veo. The scheduler asks rather than assumes.
	PollInterval() time.Duration
}

type JobHandle struct {
	Driver string // which driver can interpret Raw
	Raw    string // driver-owned; opaque above this line
}

type JobState struct {
	Status   JobStatus // Pending | Running | Succeeded | Failed
	Usage    Usage     // often only known at completion
	Err      string
	Artifact *ArtifactRef
}
```

### Artifacts

Three delivery modes collapse into one type, and a resolver normalises
them into the thing the agent can actually use: a path inside a
lobslaw storage mount.

```go
type Artifact struct {
	Kind      ArtifactKind // ArtifactURL | ArtifactInline | ArtifactMount
	URL       string       // ArtifactURL — expiring
	ExpiresAt time.Time
	Bytes     []byte       // ArtifactInline
	Mount     string       // ArtifactMount — already in operator storage
	Path      string
	MIME      string
}
```

The resolver downloads an `ArtifactURL` before `ExpiresAt`, writes an
`ArtifactInline` to a mount, and leaves an `ArtifactMount` alone. Above
that point every modality returns the same thing.

### Usage

Billing units are not tokens, so the unit is a field. Token detail
becomes one case rather than the only one.

```go
type Usage struct {
	Unit     Unit    // Tokens | Images | Megapixels | VideoSeconds | GPUSeconds | Credits
	Quantity float64
	Tokens   *TokenUsage // non-nil only when Unit == Tokens
	BilledTo Billing     // Balance | Plan
	CostUSD  float64     // zero and meaningless when BilledTo == Plan
}
```

`BilledTo == Plan` is what stops a trace reporting £0 as though it were
free: the meaningful number there is `Quantity` against the plan, not
the spend.

### Credentials

Every auth model reduces to "mutate this outbound request", which is
the narrowest waist available and hides refresh and signing entirely.

```go
type Credential interface {
	Apply(ctx context.Context, req *http.Request) error
}
```

- `static` sets a header from a resolved secret ref.
- `gcp-service-account` mints an OAuth token, caches it, and refreshes
  before expiry — reusing `CredentialService`, which already does this
  for skills.
- `aws-sigv4` signs the request.

A driver never asks what kind it holds.

---

## Adding a driver

The measure of "easy" is how many places you touch. The target is
**one file and one registry line**, and the way to keep it there is to
put every cross-cutting concern above the driver: retries, failover,
budget, policy, tracing, artifact resolution and credential refresh
are all the registry's business, not the driver's.

A new driver therefore implements only the modality methods it serves
and states its four axes. It does not know that failover exists.

### The conformance suite is the accelerator

```go
drivertest.RunConformance(t, drivertest.Subject{
	Driver:     myDriver,
	Modalities: []Modality{ModalityImage, ModalityVideo},
	Live:       os.Getenv("LOBSLAW_LIVE_DRIVER_TESTS") != "",
})
```

One shared suite every driver must pass, asserting the properties the
layer above depends on:

- a submitted job's handle survives marshal/unmarshal and still polls;
- a failed job reports `Failed` rather than blocking forever;
- usage carries a unit, and a non-token-billed call does not report
  zero;
- an expiring artifact is fetched before `ExpiresAt`;
- a transient failure is classified transient, a quota exhaustion as
  quota, a bad request as permanent;
- cancellation is honoured.

Run against mocks by default and against the real endpoint when
credentials are present. This is what makes the eighth driver a known
quantity rather than an adventure, and it is what catches a vendor
changing shape under you — which, on the evidence of this document,
they will.

### External drivers get the same interface

The skill-backed driver is **one more in-code driver** whose methods
shell out to a subprocess over the JSON contract. It is not a parallel
system: it passes the same conformance suite, resolves artifacts the
same way, and is subject to the same failover.

That is what keeps external extensibility honest. A separate external
path would drift from the in-code one, and the drift would be
discovered by an operator rather than by a test.

---

## Configuration

One table replaces `[[compute.providers]]` plus the separate
vision/audio/pdf/embedding config blocks:

```toml
[[provider]]
label    = "main"
modality = "chat"
driver   = "openai"
endpoint = "https://api.openai.com/v1"
model    = "gpt-5"
api_key  = "env:OPENAI_KEY"
backup   = "local"

[[provider]]
label    = "local"
modality = "chat"
driver   = "openai"
endpoint = "http://localhost:11434/v1"
model    = "qwen3:32b"

[[provider]]
label    = "eyes"
modality = "vision"
driver   = "anthropic"
model    = "claude-sonnet-5"
api_key  = "env:ANTHROPIC_KEY"
backup   = "eyes-local"

[[provider]]
label    = "voice"
modality = "speak"
driver   = "openai"
model    = "tts-1"
```

A provider declares exactly one modality. A single endpoint serving
several gets one entry per modality, which is more typing and much
clearer: the failover chain for vision has nothing to do with the
failover chain for chat, and an entry that claimed both would need two
backup pointers.

`capabilities` and `auto_capabilities` stay as they are — capability
discovery is orthogonal and already works. Their job narrows to
*validation*: warn when a provider declares `modality = "vision"` for a
model models.dev says has no image input.

---

## Failover, per modality

Generalise what chat already does. Each provider may name a `backup`
in the same modality; resolution walks the chain and tries each in turn
on a transient hard failure (5xx, rate limit, network refusal,
timeout). Same-turn, so the user sees the reply from whichever provider
answered.

Three rules, all of which the chat implementation already gets right
and which the generalisation must not lose:

- **Cycles rejected at config load**, not at runtime.
  `validateProviderBackups` already does this; it needs to become
  modality-aware so a chat provider cannot back up a vision one.
- **Only transient failures fall through.** A 400 is a bug in the
  request and will fail identically on the backup; falling through
  would turn one clear error into N confusing ones.
- **The chain is bounded and the walk is logged**, so an operator can
  see that they are silently running on the backup — otherwise a
  primary that has been dead for a week looks like everything is fine.

### Failure classes

What the chain does with an error comes from what the error is, not
from a retry count:

| Class | Chain | Cooldown | Log level |
|---|---|---|---|
| `transient` | advance | 30s, exponential to 5m | warn |
| `quota-exhausted` | advance | 10m | **error** |
| `credential-rejected` | advance | 15m | **error** |
| `permanent` | **abort** | none | — |

`credential-rejected` (401/403) used to classify permanent, on the
reasoning that a bad key is an operator problem and failing over spends
the backup's quota to paper over it. The concern was right and the
conclusion was not: permanent means one rotated key takes the assistant
down while two working providers sit idle, and a user's experience of a
config fault should not be "it stopped replying".

What makes advancing safe is that the fault is now loud. A credential
rejection and a spent quota both log at **error**, saying plainly that
the chain is covering for it — so the operator finds out *and* the
assistant keeps working, instead of trading one for the other.

`permanent` still aborts. A 400 fails identically everywhere, so
walking the chain would multiply one clear error into one per provider
and report the last.

### Health tracking

Trying every provider on every turn is right the first time and
wasteful the hundredth: a key revoked at 09:00 is still first in the
chain at 17:00, and every turn between pays a round-trip and a timeout
to rediscover it.

`ProviderHealth` (`internal/compute/health.go`) remembers recent
failures per provider label and the chain skips one in cooldown. One
tracker per node, shared by the chat chain and every modality chain —
a provider that just rejected the credential for `read_image` is the
same endpoint `speak` would reach.

Deliberately **not** in Raft: one node behind a broken egress proxy
must not convince the cluster that a provider is down. Each node's view
of what it can reach is legitimately its own.

Deliberately **not** a full circuit breaker either. There is no
half-open probe state, because the chain is the probe: when the
cooldown lapses the provider is tried again in its normal position, and
one success clears it. A separate prober would be a second thing that
can be wrong about a provider's health.

Two guards against over-correcting:

- A `permanent` failure never demotes. A 400 is a property of the
  request; demoting on one would let a single malformed turn take a
  healthy provider out of the chain for everybody else.
- Transient backoff is capped at 5 minutes. A chain that has written
  off every provider replies to nobody.

When every provider in a chain is in cooldown, the error says so —
"all providers failed" with no error to show reads as a bug in the
chain rather than the chain protecting itself.

---

## External drivers

Requirement: an operator can write a driver for a service lobslaw has
never heard of, in a language of their choosing, without compiling Go —
and it must not be a hole in the security model.

**An external driver is a skill.**

Skills already have every property this needs, built and tested:
signed manifests with a pinned handler digest, a Landlock/seccomp
sandbox, optional network-namespace isolation, an egress proxy with a
per-role allowlist, credential injection from the credential service,
and policy gating on invocation. The contract is already JSON on stdin
and stdout, which any language can satisfy.

The manifest gains a declaration:

```yaml
name: replicate-video
version: 1.0.0
runtime: python
handler: driver.py
handler_sha256: …
provides:
  modality: video
credentials:
  - { provider: replicate }
```

A skill that declares `provides.modality` is registered as a provider
for that modality rather than as a bare tool, and is invoked with the
modality's JSON request shape — the same shape the compiled drivers
implement.

Why not the alternatives:

- **MCP servers.** Fine for tools, and they stay available for that.
  But MCP has no signing and no sandbox story, and a driver holds
  provider credentials and sits on the hot path. The boundary skills
  already enforce is the one this needs.
- **Go plugins.** In-process, so no boundary at all, plus exact
  toolchain matching. Rejected on both counts.
- **A bespoke driver plugin protocol.** Would duplicate signing,
  sandboxing, egress control and credential injection — four things
  that are hard to get right and are already right.

### Revised: declarative first, code second

The section above stands as the answer to *"how do we run someone
else's driver code safely"*. It is the wrong thing to build first,
for three reasons.

**The cost claim above was wrong.** It said a subprocess spawn is
"irrelevant for generation modalities, which are seconds-to-minutes
operations". Polling is repeated: a five-minute video at a fifteen
second interval is roughly twenty invocations, each a fresh
mount/pid/net/user namespace, seccomp-bpf filter, cgroup and nftables
ruleset. Tolerable, but not irrelevant.

**A skill is one handler; JobDriver is three operations.** Submit,
Poll and PollInterval would dispatch on an `op` field inside the JSON
contract — a protocol inside a protocol, which every external author
then has to implement correctly.

**Most of the demand is not for code.** "Support a vendor lobslaw has
never heard of" is usually the same protocol at a different URL, which
already works via `driver = "openai"` plus an endpoint. What actually
varies is narrower than a driver:

| varies between vendors | needs |
|---|---|
| endpoint URL | config — already supported |
| auth header shape | config — already supported |
| request field names | a mapping |
| response field paths | a mapping |
| async protocol | real code |

So the first increment is a **declarative template driver**: endpoint,
auth style, and request/response field paths in TOML, interpreted by
one compiled driver. It covers the long tail of OpenAI-shaped clones
with **no code execution at all** — nothing to sign, sandbox, spawn or
get wrong — and it is far smaller than the skill path.

Skills remain the answer for a genuinely different protocol, scoped to
the asynchronous modalities where repeated spawns amortise over
minutes. They are not offered for `chat` or the other infrastructure
modalities at any point.

**Sequencing.** Neither is scheduled. Each of the three generation
modalities currently has exactly one vendor, so a second vendor per
modality proves the waist again and is useful immediately, whereas an
extension point proves nothing until somebody extends it. Build the
template driver when a vendor appears that config cannot reach; build
the skill path when one appears that a mapping cannot describe.

---

## Mocks

Every driver ships a mock, and `mock` is itself a selectable driver:

```toml
[[provider]]
label    = "test-vision"
modality = "vision"
driver   = "mock"
```

The requirement this serves: **a full node must be bootable with every
modality mocked and no network at all.** That is the prerequisite for a
turn-level end-to-end test — send a message in, assert reply,
transcript, memory and trace come out — which is the class of test
lobslaw is currently missing, and the class that would have caught
several wiring regressions that unit tests did not.

`MockProvider` today is chat-only and scripted. Generalising it is the
first task of this work, not the last, because everything else is
easier to test once it exists.

---

## Sequencing

The ordering principle: **prove the abstraction against what already
works before betting new work on it.** If the interface cannot express
the four modalities already shipping, it is wrong, and that is far
cheaper to discover during a migration than during a new build.

1. **The waist.** Driver interfaces, `Usage` with a unit, `Credential`,
   `Artifact`, `JobHandle`. Types and the conformance suite only —
   no behaviour change.
2. **Mock driver for every modality**, passing conformance. This comes
   before the migration, not after, because it is what makes the
   migration verifiable.
3. **Migrate the four existing modalities** — chat, embedding, vision,
   document/audio — onto the waist, replacing the four format enums.
   Behaviour-preserving. **This is the step that validates the design**,
   and if something does not fit, the interface changes here while it
   is still cheap.
4. **The single `[[provider]]` table**, with the existing per-builtin
   config shimmed for one release.
5. **The no-network end-to-end harness** — a full node on mock drivers,
   serving a real turn. Answers the testing question directly.
6. **Per-modality failover**, three classes, finishing what
   `SelectByCapability` was written for.
7. **Async job plumbing**: `JobDriver`, artifact resolution, and the
   commitment-backed poll handler.
8. **New modalities**: `speak` (sync), then `image`, then `video` —
   in ascending order of how much of step 7 they exercise.
9. **External drivers via skills**, passing the same conformance suite.

Steps 1–5 add no capability and are the whole point: they are what
make step 8 a day's work per modality instead of a fresh argument each
time. Step 3 is the checkpoint — if the abstraction survives contact
with four existing implementations, it will survive the fifth.
