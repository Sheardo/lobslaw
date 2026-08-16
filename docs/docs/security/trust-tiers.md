---
sidebar_position: 5
---

# Trust tiers

Every provider you configure gets to see whatever you send it. `trust_tier` is where you write down
how much that matters, and `min_trust_tier` is where you say what you will accept.

```toml
[[compute.providers]]
label      = "ollama"
trust_tier = "local"
```

```yaml
# SOUL.md
config:
  min_trust_tier: private
```

**Higher is more trusted.** A floor of `private` admits `private` and `local`, and refuses `public`.

| Name | Value | Meaning |
|---|---|---|
| `local` | 100 | inference on hardware you control; content never leaves the host |
| `private` | 50 | a third party under a contract excluding training on submitted data |
| `public` | 1 | anything else |
| — | 0 | reserved: *unset*. Never write it |

Write a name or a number. Numbers exist for the cases the names do not fit: a model on a VPS you
rent is not `local` — the hardware is somebody else's — and it is plainly better than a public API
with no contract. `trust_tier = 60` says exactly that, and a floor of `55` admits it.

## The problem it actually solves

Nobody accidentally types the wrong provider into their config. You would notice.

The hazard is **routing you did not author**. lobslaw has failover chains, capability-based
selection for vision and audio, and backup providers. All of that exists to keep the assistant
working when something breaks — and every one of those mechanisms is a path by which your content
reaches a provider you did not pick for this turn.

The floor is the constraint that survives those mechanisms. Without it, "my primary is private" is
true right up until the primary returns a rate-limit error, at which point the turn completes
somewhere else and nothing says so.

## Where it is checked

- **At boot.** A provider below the floor stops the node starting. You find out from a config error
  rather than from a turn.
- **On every chat request**, at every candidate in the failover chain — not just the first.
- **On every modality chain**: vision, audio, PDF, speak, image. A vision provider is handed your
  image and a speak provider the text of the reply; they are not lesser recipients of content.

The boot check and the runtime check are not redundant. `min_trust_tier` lives in `SOUL.md`, which
can be tuned while the node runs — so raising the floor after boot has to take effect in the routing
and not only in the prompt.

## Omitting it

An omitted `trust_tier` means **nobody said**, not `public`. Those are different facts and lobslaw
keeps them apart.

While no floor is set, an untiered provider is unremarkable. The moment you set `min_trust_tier`,
every provider needs an explicit tier or the node refuses to start — an undeclared tier is not
evidence of a high one, and quietly treating it as one would be the wrong direction to guess in.

`min_trust_tier` itself, unset, means no floor at all. It is opt-in.

## Getting it wrong is a config error, loudly

| You wrote | What happens |
|---|---|
| `trust_tier = "privat"` | boot fails, naming the value |
| `trust_tier = 500` | boot fails; the range is 1–100 |
| `trust_tier = "primary"` | boot fails — this was never a tier, despite older docs |
| provider below the floor | boot fails, naming provider and floor |

There is deliberately no way to define your own tier names. A custom-name scheme turns a typo into
a silent extra tier, and silence is the one failure mode a floor must not have.

## What it is not

The floor governs where your content goes **among the providers you configured**. It says nothing
about what a provider does with it after it arrives, and it cannot.

`trust_tier = "private"` is *your assertion* about a commercial contract. lobslaw cannot read that
contract, cannot verify your `local` endpoint is not quietly proxying somewhere, and does not try.
Like the [hardline floor](./hardline-floor.md), this reduces accidents and gives an unambiguous
stop signal — it is not a boundary that holds against a determined adversary with access to your
config.
