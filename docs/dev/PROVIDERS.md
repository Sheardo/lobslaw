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

Naming this resolves the question of how to support "custom variants
such as qwencloud". Qwen Cloud is not a driver — it is a *provider*
using the `openai` driver at a different endpoint with a different
model name. That is already how `LLMClient` works; it has simply never
been said out loud, which is why each modality reinvented the
distinction under a different name.

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

The cost of this choice is a subprocess spawn per invocation. That is
irrelevant for generation modalities, which are seconds-to-minutes
operations. It would matter for `chat`, so external drivers are
**not** offered for the infrastructure modalities.

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

1. **Unify the driver concept.** One `Driver` type replacing
   `VisionFormat` / `AudioFormat` / `EmbeddingFormat` /
   `ProviderConfig.Format`. Mechanical, and it is what makes the rest
   expressible.
2. **Modality-keyed provider registry** and the single `[[provider]]`
   table, with the existing per-builtin config kept working via a
   compatibility shim for one release.
3. **Mock driver for every modality**, then the end-to-end harness.
4. **Per-modality failover**, generalising `Backup` and finishing what
   `SelectByCapability` was written to support.
5. **New modalities**: `speak`, `image`, `video` as tools.
6. **External drivers via skills.**

Steps 1–3 are refactoring plus test infrastructure and unlock the
rest. Step 5 is where new capability actually appears, and it is
deliberately last, because adding modalities before the layer is
unified means adding a fourth format enum and a fifth config block.
