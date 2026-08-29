---
sidebar_position: 3
---

# Policy Engine

The gate every tool call passes through.

## What it does

For every `Executor.Call(ctx, tool, args)`, the policy engine asks:

> Given these claims, this action, this resource, and the rules in the store — **allow**, **deny**, or **require_confirmation**?

If the answer is `deny`, the call returns an error before the tool runs. If it's `require_confirmation`, the call is paused until a human-in-the-loop confirms via the originating channel. Allow is silent and proceeds.

## When a condition cannot be evaluated

A rule's conditions can fail to evaluate in two ways: the key names an evaluator this build does
not have, or a registered evaluator returns an error. Either way the engine does not know whether
the rule matched, and the effect decides what happens:

| Effect | On evaluation error |
|---|---|
| `deny` | **Applied.** Skipping it would drop exactly the protection the rule exists for, and evaluation would continue into whatever lower-priority allow sits underneath. |
| `require_confirmation` | **Applied**, for the same reason. |
| `allow` | **Skipped.** This is fail-closed: applying an allow yields the most permissive effect there is, so whatever sits below — a deny, a confirmation, or default-deny — is never a wider grant. |

Skipping an erroring allow is deliberately *not* a hard deny. A hard deny would turn one flaky
evaluator into a total outage while providing no additional safety, since the rules below it
evaluated cleanly on their own merits.

### The boot audit

No condition evaluator is registered in lobslaw today, so **every** conditioned rule is currently
unevaluable. At decision time that is handled safely, but silently — an operator's time-of-day
allow looks correct in a listing and simply never grants.

`Engine.LogUnevaluableRules()` runs at boot and says so at error level, naming each rule, the
condition keys it cannot resolve, and what the rule actually does:

```
policy: unevaluable rule rule_id=office-hours-allow effect=allow
  condition_keys=[time_of_day]
  consequence="this rule will never grant anything; requests fall through to
               lower-priority rules and ultimately to default-deny"
```

Registering the evaluator clears the defect.

## Approval-minted rules

An "always" approval mints a rule rather than writing to a second store beside the engine. The
engine already answers `(subject, action, resource)`, and two things deciding the same question
eventually disagree.

Such a rule carries `created_by = "approval:<prompt_id>"`, which is what makes the grant findable
and revocable as a class:

```
lobslaw policy approvals                        # list them
lobslaw policy revoke-approvals --apply         # revoke all
lobslaw policy revoke-approvals approval:p1 --apply
```

Both are offline subcommands — the node must be stopped, as with `lobslaw memory`.
`revoke-approvals` is a dry run unless `--apply` is given, and refuses to delete any rule an
operator wrote.

Constraints on minting, all enforced in `internal/policy/approval_rules.go`:

| Rule | Why |
|---|---|
| Priority 1 | Below any operator-authored deny. An approval is one tap; it should not outrank a rule somebody wrote deliberately. |
| No wildcards in action or resource | The button offered the operation the user saw, not a class of them. This is about meaning, not syntax — a resource transformed to drop part of what the user read (approving `git status --short` and minting `git status`) is the refused thing wearing a disguise, and is not done anywhere. |
| Subject must be `user:` / `role:` / `scope:` | Anything else fails closed in `subjectMatches`, so the rule would look like a grant in a listing and grant nothing. |
| Refused if the [hardline floor](/security/hardline-floor) denies the resource | Checked at mint time as well as at invoke time, so a listing never shows a grant that reads as though it works. |
| Id derived from the prompt id | Re-tapping is idempotent rather than piling up duplicates. |

## Per-command shell approval

`shell_command` is asked about twice: once as a tool (`tool:exec` / `shell_command`, which every
node allows by default seed) and once as a command (`shell:run` / the command itself). The second
question is the one that matters, and it uses its own action deliberately — reusing `tool:exec`
would mean the default seed satisfied the gate before it was asked.

The resource is **the exact command**, canonicalised for whitespace and quoting only. Nothing is
dropped, so `git status --short` and `git push --force` are different grants. Tapping *Always
allow* therefore stops one command from being asked about, not the shell.

Some commands have no stable identity and are asked about **every time**, with no scope button
offered: anything containing a pipe, `&&`, `;`, a redirect, `$`, backticks, a glob, or a `VAR=`
prefix. What runs depends on the environment or on more than one program, so no grant could
honestly name it. Those are evaluated under the reserved resource `!unclassified`.

To stop being asked about a family of commands, write a rule. This is the deliberate,
visible, revocable form of "generalise", and it is the answer to *"I don't want to approve every
git command"*:

```toml
[[policy.rules]]
id       = "james-git-is-fine"
priority = 20
effect   = "allow"
subject  = "user:tg-@james"
action   = "shell:run"
resource = "git *"          # prefix glob
```

An `allow` on `!unclassified` is the explicit "stop asking me about compound commands". No real
command can reach that resource, so it cannot be hit by accident.

The [hardline floor](/security/hardline-floor) still applies first and is not reachable by any of
this: `rm -rf /`, fork bombs, `mkfs`, `curl | sh` are refused before a prompt is ever raised, and
`Mint` refuses to write a rule for them even if one were somehow requested.

## Inputs

```go
type EvaluateInput struct {
    Claims   *types.Claims     // who is calling: scope, user_id, channel
    Action   string            // "tool:exec", "credentials:read", ...
    Resource string            // tool name, credential ID, ...
    Context  map[string]string // optional turn context
}
```

The action+resource shape is the matching key. Conventions:

| Action | Resource shape | Used for |
|---|---|---|
| `tool:exec` | tool name (`current_time`, `notify`, `gws-workspace.gmail.send`) | Every agent tool call |
| `shell:run` | the exact command (`git status --short`) | Every `shell_command` call, asked separately from `tool:exec` |
| `memory:write` | record kind (`episodic`) | Staging agent-initiated memory writes |
| `credentials:read` | credential ID | `credentials_grant` invoker side |
| `credentials:grant` | role / skill name | granting a skill access |
| `oauth:start` | provider name | starting a device flow |
| `clawhub:install` | bundle path | installing skill bundles |

`tool:exec` is the dominant action; the others are mutator-specific.

## Rule shape

A rule is a TOML `[[policy.rules]]` block:

```toml
[[policy.rules]]
id          = "owner-soul-tools"
description = "Owner can mutate soul fragments"
priority    = 20
effect      = "allow"             # allow | deny | require_confirmation
subject     = "scope:owner"       # kind:value — see Subject matching below
action      = "tool:exec"
resource    = "soul_*"             # glob — * prefix or suffix
```

**Subject matching** uses `kind:value` form. Common kinds:

- `scope:owner`, `scope:public` — scope claims
- `user:alice` — specific user ID
- `channel:telegram` — channel type
- `subject:google:1234567890` — OAuth subject

Multiple rules per request? The engine sorts by `priority` (descending) and takes the first match's effect. If nothing matches and the resource has a default-allow seed (built-in tools at priority 1), allow. Otherwise deny.

**Priorities, by convention:**

| Range | Use |
|---|---|
| 1 | Default-allow seeds (built-in tools) |
| 10 | Default-deny seeds (sensitive built-ins) |
| 20–99 | Operator-declared allow rules |
| 100+ | Operator-declared overrides + `require_confirmation` for risky tools |
| 1000+ | Hard denies (e.g. revoked subjects) |

A higher number wins. Within the same priority, the engine is deterministic (sort by id) but you should never rely on it — pick distinct priorities.

## Default seeds

On first boot, `internal/node/wire_seeds.go` writes a fixed set of rules:

- **Allow** every `BuiltinScheme` tool (the in-process built-ins) at priority 1.
- **Deny** every sensitive built-in (`oauth_*`, `credentials_*`, `clawhub_install`, `soul_*`) at priority 10. The operator overrides these with priority-20 allows in `config.toml`.

Skills, MCP servers, and clawhub-installed tools are **not** seeded. They're invisible to the agent until the operator adds an allow rule:

```toml
[[policy.rules]]
id       = "owner-can-call-gws-workspace"
priority = 20
effect   = "allow"
subject  = "scope:owner"
action   = "tool:exec"
resource = "gws-workspace.*"
```

## `require_confirmation`

For destructive tools (anything that writes off-host, sends a message, modifies external state), prefer:

```toml
[[policy.rules]]
id       = "confirm-on-write"
priority = 50
effect   = "require_confirmation"
subject  = "scope:owner"
action   = "tool:exec"
resource = "gws-workspace.gmail.send"
```

The engine pauses the call, asks the originating channel for `[Yes / No]`, and proceeds based on the human's reply. This is the **primary defence against prompt injection** for write tools — narrower than blocking, narrower than sandbox.

## Why skills can't impersonate built-ins

Built-in tools live under `BuiltinScheme://` paths. The `internal/compute/registry.RegisterExternal` rejects any registration whose Path begins with that scheme — so a skill manifest claiming `path = "builtin://current_time"` is rejected at install time.

This means the priority-1 default-allow seed for built-ins never applies to non-built-in code. Skills, MCP, and clawhub-installed tools always traverse the operator-declared ruleset.

## Audit

Every policy evaluation that results in a `tool:exec` produces an audit record:

```json
{"ts":"2026-04-28T13:45:01Z","action":"tool:exec","resource":"clawhub_install","subject":"scope:owner","decision":"allow","matched_rule":"owner-clawhub-install","duration_ms":1.4}
```

These land in `audit/audit-YYYYMMDD.jsonl` and (if `[audit.raft]` is set) replicate via Raft to peers. See [Operating → Audit](/operating/cli) for retrieval.

## Hot reload

`SIGHUP` reloads `config.toml`, including the `[[policy.rules]]` blocks. New rules apply on the next call; in-flight calls keep the rules they evaluated against. There is no "restart needed" workflow for policy changes.

## Reference

- `internal/policy/engine.go` — evaluation core
- `internal/policy/rules.go` — TOML schema
- `internal/node/wire_seeds.go` — default seeds
- `internal/audit/` — audit log writer
