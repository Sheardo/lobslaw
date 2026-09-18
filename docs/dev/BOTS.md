# Bots — a principal, not a permission set

How lobslaw names more than one agent, and why a bot's personality
grants it no authority.

## TL;DR

A **bot is a principal**. Everything that already decides against a
principal — memory ownership, policy subjects, scheduled-task owners,
audit entries — inherits per-bot isolation for free. A bot's authority
is NOT a field on its record; it runs as `bot:<id>` and the policy
engine decides what that may do, exactly as for any other subject.

Persona (instructions, soul overlay) is how the bot answers, not what
it may do.

| Piece | Where | Shape |
|---|---|---|
| Registry | `internal/memory/bots.go` | Raft record, revision-checked CAS |
| Ownership | `BotRecord.owner` | Explicit human `user:<id>`; empty = nobody |
| Personality | `internal/soul` | One overlay per bot; chief keeps `soul:tune` |
| Turns | `internal/compute.TurnIdentityFor` | Mint `bot:<id>`; never `Resolve()` it |

Turns without `BotID` behave as the main assistant.

See aide decisions `owned-bots` and `compute-teams`.

---

## Teams (opt-in: `FunctionComputeTeams`)

Coordinator selection, specialist delegation and a durable inbox.
Off by default. `--all` does not enable it. Ordinary compute does
not seed teams or register team tools.

| Piece | Where | Shape |
|---|---|---|
| Team | `GroupRecord` | Single human owner; empty = inaccessible |
| Coordinator | `GroupRecord.coordinator_bot_id` | An ordinary bot with that role |
| Inbox | `BotInboxItem` | Raft queue, `LOG_OP_CLAIM`, drain loop |
| Delegation | `ask_bot` / `tell_bot` / `inbox_post` | Registered only when teams is on |

```mermaid
flowchart TB
  subgraph gate ["FunctionComputeTeams"]
    Groups[GroupRecord]
    Inbox[BotInboxItem]
    Tools["ask_bot / tell_bot / inbox_post"]
    Drain[inbox drain loop]
  end
  Channel[Telegram / Slack / REST] -->|BotID on turn.Request| Agent
  Channel -->|binding or this user's coordinator| Groups
  Tools --> Inbox
  Drain --> Agent
  Agent -->|WrapContext untrusted| Peer[peer bot text]
```

Empty group owner is nobody, never public. Channel routing uses an
explicit binding or **that user's** coordinator. A missing binding
does not fall into another person's team.

`ask_bot` draws on the caller's budget, strips itself from the child
registry (`Without("ask_bot")` — empty allowlist does not re-grant
it), and fails closed if the child would need confirmation. Peer text
is wrapped with `promptgen.WrapContext`. `TurnBudget.Tighten` runs on
both `Run` and `Resume`.

`bot_*` / `ask_bot` / `tell_bot` / `inbox_*` join `noSeedTools`
(default-deny like `soul_*`). Restore mode pauses the drain.

---

## The shape

```mermaid
flowchart LR
  subgraph record [BotRecord]
    ID["id: engineering"]
    Owner["owner: user:alice"]
    Brief[instructions]
    Tools[tools allowlist]
  end
  subgraph principal [Principal]
    BotP["bot:engineering"]
  end
  subgraph overlay [Soul overlay]
    Key["soul:tune:engineering"]
  end
  ID --> BotP
  ID --> Key
  Owner -->|"MayModify"| Human["user:alice"]
  Brief --> Prompt[system prompt]
  Tools -->|"registry filter"| Model[tools the model sees]
  BotP --> Policy[policy engine]
```

The tools list is a **registry filter**: an absent tool is unexpressible
rather than merely denied. Policy is still the thing that says no.
Empty tools means the node default set, not "no tools".

---

## Identity

`identity.Bot(id)` mints `bot:<id>`. It is never reached from
`Resolver.Resolve`. The alias map translates ids that arrived FROM a
channel; putting `"bot:devops"` through Resolve yields
`"user:bot:devops"`, which owns nothing the bot owns and matches no
rule written about it.

```mermaid
sequenceDiagram
  participant Turn
  participant Agent
  participant Resolver
  Turn->>Agent: BotID=devops, Claims.UserID=bot:devops
  Agent->>Agent: identity.Bot("devops")
  Note over Agent: minted, not resolved
  Agent-->>Turn: Principal=bot:devops
  Turn->>Agent: BotID=devops, Claims.UserID=alice
  Agent->>Resolver: Resolve("alice")
  Resolver-->>Agent: user:alice
  Note over Agent: the bot did the work; alice owns it
```

A turn with no `BotID` is unchanged: `Resolve(userID)` as before.

---

## Ownership

`BotRecord.owner` is a human principal (`user:<id>`). Empty owner is
nobody — the record is inaccessible, never public. There is no
unowned-editable fallback.

`MayModify` rejects an empty principal **and** an empty owner.

On boot, unowned records are adopted onto the unique `[[user]]` with
`role:operator`. None or more than one operator leaves them
inaccessible (logged). Owner, once set, is preserved across updates.

---

## Personality overlay

The chief's overlay key is the exact pre-existing bytes `soul:tune`.
Changing those bytes would wake an upgraded cluster with a default
personality. Other bots get a suffixed key. Their overlay merges onto
the operator's `SOUL.md` **without** the chief overlay underneath —
"be less sarcastic with me" said to the chief must not retune devops.

```mermaid
flowchart TD
  Baseline["operator SOUL.md"]
  ChiefKey["soul:tune"]
  EngKey["soul:tune:engineering"]
  Baseline --> ChiefTurn[chief / empty BotID]
  ChiefKey --> ChiefTurn
  Baseline --> EngTurn[bot:engineering]
  EngKey --> EngTurn
  ChiefKey -.->|"must not leak"| EngTurn
```

`SoulTuneRecordIDFor("")` and `SoulTuneRecordIDFor(ChiefBotID)` both
return `SoulTuneRecordID`. A store that does not implement
`BotTuneStore` falls back to the chief overlay, so a deployment that
never creates a bot is unchanged.

---

## Persistence

Writes go through Raft (`LOG_OP_CLAIM`) with the same revision CAS as
soul tune. `BucketBots`, `BucketGroups` and `BucketBotInbox` are in
`archiveKinds`; credentials and browser sessions are not. Restore mode
pauses the inbox drain.

Deleting a bot does not cascade the records it owned. Recreating the
same id restores the principal.

---

## Out of scope

- Browser console / SPA (`web/`, `FunctionUIWeb`)
- A default team seeded on ordinary compute
- Per-bot channel tokens / avatars
