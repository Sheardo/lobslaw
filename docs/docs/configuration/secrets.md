---
title: Secrets
description: Resolve keys and tokens from Bitwarden, 1Password, or any local vault instead of the node's own disk.
---

# Secrets

Every `_ref` field in lobslaw takes a **secret reference**, never a secret. A literal is refused outright, so a plaintext key cannot be committed in a config file.

Two references always work and resolve against this machine:

| Reference | Resolves to |
|---|---|
| `env:VAR_NAME` | that environment variable |
| `file:/path/to/secret` | the file's contents, trailing newline stripped |

Anything else is a **provider label** you declare, which is how a key lives in a vault instead of on every node's disk.

## Declaring a provider

A provider's `label` *is* the reference scheme it answers to. Declare one and `bw:app/key` works anywhere `env:APP_KEY` works today:

```toml
[[secrets.providers]]
label  = "bw"
driver = "bitwarden"
env    = { BW_SESSION = "env:BW_SESSION" }

[[compute.providers]]
label       = "openrouter"
api_key_ref = "bw:lobslaw/openrouter"     # ← the vault, not the disk
```

Three drivers ship:

| `driver` | Backend | Notes |
|---|---|---|
| `bitwarden` | the `bw` CLI | defaults to the item's `password` field; `options.field` selects another |
| `onepassword` | the `op` CLI | path is `vault/item/field` — the `op://` URI without its scheme |
| `exec` | any command | the long tail: `pass`, `gopass`, `sops`, `age`, `systemd-creds` |

### Any local vault

`exec` runs a configured argv and takes its stdout, so a tool nobody has written a driver for needs no Go:

```toml
[[secrets.providers]]
label   = "pass"
driver  = "exec"
command = ["pass", "show", "{{path}}"]

[[secrets.providers]]
label   = "sops"
driver  = "exec"
command = ["sops", "--decrypt", "--extract", "{{path}}", "secrets.enc.yaml"]
```

`{{path}}` is replaced with whatever follows the scheme in the reference. With no placeholder anywhere in the argv, the path is appended as a final argument — which is what `pass show <path>` wants anyway.

It is an **argv, never a shell string**. A secret path containing a space must not be able to become a second command.

| Option | Default | Meaning |
|---|---|---|
| `trim_whitespace` | `true` | strip surrounding whitespace from stdout |

The default is on because a CLI that prints a trailing newline is the norm, and a key with `\n` on the end fails authentication in a way nothing reports usefully. Turn it off for the rare secret whose newline is load-bearing.

## The bootstrap floor

Three things **must** use `env:` or `file:`, and are refused otherwise with an error saying so:

- `memory.encryption.key_ref`
- the `[cluster.mtls]` paths
- any `[[secrets.providers]]` credential

This is not a policy choice, it is the order things happen in. The memory key is resolved before the node is constructed — before any wiring stage exists, including the one that would build a provider. And a vault whose own credential came from another vault needs one of them working before either does.

The error names the constraint rather than reporting an unknown scheme, because *"unknown scheme: bw"* is a confusing thing to read when `bw` is configured and working three lines further down the same file.

## Checking it works

`lobslaw doctor` builds every declared provider **and resolves one real reference through each**:

```
OK    secret providers: bw ✓, pass ✓
```

Constructing a provider is not enough to know it works — a missing binary, a locked vault and an expired session all construct fine and fail at the first fetch, which on this node happens during boot. The probe uses a reference your config actually contains rather than one invented for the check.

## Caching

A resolved value is reused for `secrets.cache_ttl` (default 5 minutes). One boot resolves the same reference several times — the chat driver, the capability probe and doctor all read the same provider key — and on a CLI-backed vault each of those would otherwise be a separate process.

## What this is not

`[[secrets.providers]]` is about where **lobslaw's own configuration** gets its secrets.

It is unrelated to the OAuth credential store (`internal/memory/credentials.go`), which holds credentials **the agent's skills use on your behalf** — encrypted with the cluster key, never decrypted into the agent's context, gated per skill. The two are deliberately separate and a vault provider is not a second route into that bucket.

## Known limitation

Secret-provider subprocesses do **not** route through the smokescreen egress proxy. A `bw` or `op` CLI reaching its cloud egresses directly.

The reason is the bootstrap floor above: some resolution happens before the egress stage exists, so a proxy cannot be applied uniformly — and applying it to some resolutions and not others would be worse than applying it to none. Tracked in `DEFERRED.md`.

## Rotation

Not yet. Secrets resolve at boot into constructed drivers, so rotating one in the vault takes a restart — which is what it took before providers existed too.
