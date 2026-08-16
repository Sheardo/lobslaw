---
sidebar_position: 4
---

# Providers

The LLM provider router. Each `[[compute.providers]]` block declares one upstream model.

## Minimum

```toml
[[compute.providers]]
label       = "openrouter"
endpoint    = "https://openrouter.ai/api/v1/chat/completions"
api_key_ref = "env:OPENROUTER_API_KEY"
model       = "anthropic/claude-sonnet-4"
```

That's enough to get a working chat agent.

## Capability declaration

```toml
[[compute.providers]]
label             = "minimax"
endpoint          = "https://api.minimax.chat/v1/text/chatcompletion_v2"
api_key_ref       = "env:MINIMAX_API_KEY"
model             = "MiniMax-M2"
capabilities      = ["chat"]                     # explicit: text-only
auto_capabilities = false
```

Capabilities determine which builtins route to this provider:

- `chat` — agent loop default
- `vision` — `read_image` builtin
- `audio-transcription` — `read_audio` builtin
- `pdf` — `read_pdf` builtin
- `embedding` — embedder
- `function-calling` — tool-using turns

The agent uses the most-trusted provider that has the required capability. If your council needs vision, every provider in `[compute.roles] council` either declares `capabilities = ["vision"]` or you explicitly pin per-role:

```toml
[compute.roles]
vision = "openrouter"   # send vision turns through openrouter regardless of default
```

## models.dev auto-discovery

```toml
[[compute.providers]]
label             = "openrouter"
endpoint          = "https://openrouter.ai/api/v1/chat/completions"
api_key_ref       = "env:OPENROUTER_API_KEY"
model             = "anthropic/claude-sonnet-4"
auto_capabilities = true
```

When `auto_capabilities = true`, lobslaw fetches models.dev's catalog at boot and intersects all entries for that model. **Declared capabilities always win on conflict** — operator authority is preserved.

Conservative discovery: when a model name appears in multiple provider listings, the **intersection** is taken — only claim a capability when every catalog entry agrees. This guards against catalog bugs (e.g. one source incorrectly tagging MiniMax-M2 as multimodal when every other source listed it correctly as text-only).

## Trust tiers

`trust_tier` declares **how exposed your content is** when a provider handles it. It is not a role,
and it has nothing to do with which provider runs first — that is `roles.main` and `backup`.

```toml
[[compute.providers]]
label      = "ollama"
trust_tier = "local"          # 100

[[compute.providers]]
label      = "anthropic"
trust_tier = "private"        # 50

[[compute.providers]]
label      = "my-vps"
trust_tier = 60               # between private and local

[[compute.providers]]
label      = "openrouter"
trust_tier = "public"         # 1
```

**Higher is more trusted.**

| Name | Value | Meaning |
|---|---|---|
| `local` | 100 | inference on hardware you control; content never leaves the host |
| `private` | 50 | a third party under a contract excluding training on submitted data |
| `public` | 1 | anything else |
| — | 0 | reserved: *unset*. Never write it |

You can write a name or a number. The names are reserved points on the scale, and numbers exist for
the cases they do not fit — a model on a VPS you rent is not `local`, because the hardware is
somebody else's, and it is plainly better than a public API with no contract. `trust_tier = 60` says
exactly that.

An unrecognised **name** is a config error, not a new tier. A number outside 1–100 is a config error
too. Both fail at boot with the offending value named.

### The floor

Set a floor in `SOUL.md` and providers below it are never used:

```yaml
config:
  min_trust_tier: private     # or 50
```

Every provider at or above the floor is eligible. Everything below is refused — including as a
*backup*, which is the case that matters: failover exists to rescue a turn when the primary fails,
and without the floor it would rescue it onto whatever provider happened to be next.

The floor is enforced on the chat chain, on every modality chain (vision, audio, PDF, speak, image
— a vision provider is handed your image, and a speak provider the text of the reply), and at boot.
A provider below the floor stops the node starting, so you find out from a config error rather than
from a turn.

### If you omit it

An omitted `trust_tier` means *nobody said*, not *public*. That is fine while no floor is set. The
moment you set `min_trust_tier`, every provider needs an explicit tier or the node refuses to start
— an undeclared tier is not evidence of a high one.

`min_trust_tier` itself, unset, means no floor at all.

### What it does not do

The floor governs where content goes **among the providers you configured**. It says nothing about
what a provider does with your content after receiving it, and it cannot: `trust_tier` is your
assertion about a contract, not something lobslaw can verify.

## Endpoints

`endpoint` should be the full URL up to and including the chat completions path. Common forms:

| Provider | Endpoint |
|---|---|
| OpenRouter | `https://openrouter.ai/api/v1/chat/completions` |
| Anthropic | `https://api.anthropic.com/v1/messages` |
| OpenAI | `https://api.openai.com/v1/chat/completions` |
| MiniMax | `https://api.minimax.chat/v1/text/chatcompletion_v2` |
| Local Ollama | `http://localhost:11434/v1/chat/completions` (requires `egress_allow_private_ranges = true`) |
| vLLM / TGI | whatever you wired |

The endpoint hostname is automatically added to the `llm` egress role's allowlist.

## Embeddings

```toml
[compute.embeddings]
endpoint    = "https://openrouter.ai/api/v1/embeddings"
api_key_ref = "env:OPENROUTER_API_KEY"
model       = "openai/text-embedding-3-small"
dimensions  = 1536
```

The embedder is used for episodic memory recall, semantic search, and dream synthesis. Match `dimensions` to your model — mismatches will silently produce garbage results.

If you change embedding model after data already exists in the index, run:

```bash
lobslaw backfill-embeddings --config config.toml
```

This re-embeds every record with the new model. Without it, recall quality collapses.

## Roles

```toml
[compute.roles]
worker  = "openrouter"
council = ["openrouter", "anthropic-direct", "minimax"]
vision  = "openrouter"
```

| Role | Used by |
|---|---|
| `worker` | individual research workers (parallel sub-agents) |
| `council` | `council_review` builtin |
| `vision` | vision turns when default provider lacks capability |
| `audio` | audio turns |
| `pdf` | pdf turns |

## Reference

- `internal/compute/providers.go` — `ProviderRegistry` + capability matching
- `internal/modelsdev/` — catalog fetcher + cache
- `internal/compute/capability_modelsdev.go` — auto-capability merger
- `pkg/config/config.go` — schema (`ProviderConfig`, `ComputeConfig`)
