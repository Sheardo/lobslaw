---
sidebar_position: 1
---

# Bots

lobslaw starts as one assistant. You can give it colleagues.

The one you already talk to becomes the **coordinator**, keeping the
personality and memory it already had — nothing about your first
conversation after upgrading is different. What changes is that it can
create specialists, hand them work, and tell you what they did.

## Making one

Ask, on whatever channel you use:

> Make me a devops bot. It watches the Kubernetes cluster, and it
> should use `shell_command` and `read_file` but nothing else.

Or use the [web console](#the-web-console). Either way the bot gets:

| | |
|---|---|
| **A brief** | "You are the engineer for XYZ, you own the build and the deploy pipeline." Standing configuration, not a task — write it about the role. |
| **Its own memory** | What marketing learns does not surface in devops' recall. |
| **Its own tools** | A tool you leave out is one the bot is never even *shown*. Leave the list empty and it gets everything the node has. |
| **Its own inbox** | Below — this is most of the point. |

Bot ids are lowercase slugs and cannot be changed: the id *is* the
bot's identity, so renaming one would orphan everything it owns.

## The inbox

Every bot has a durable queue. You put work in; the bot pulls the next
item, does it, and writes the outcome back.

- **Work survives.** Restart the node, lose a leader, disable and
  re-enable the bot — the task is still there.
- **Results have somewhere to live.** "Deploy staging" comes back with
  what version went live, attached to the task you asked about.
- **Nothing disappears.** A failed task is retried; out of retries it
  sits there marked *failed* with the reason on it, and a retry button
  beside it.

Items are worked highest-priority first, then oldest first.

## Bots talking to each other

Two ways, and which one to use depends on whether you need the answer
now.

| | `ask_bot` | `inbox_post` |
|---|---|---|
| Waits for an answer | Yes, within the turn | No |
| Good for | "Is this technical claim accurate?" | "Deploy staging and tell me the version" |
| Costs | The asker's own budget | Its own turn later |
| Survives a restart | No | Yes |

You grant connections explicitly:

> Let marketing ask engineering things.

Loops are refused **when you set them up**, not when they happen, and
the error names the path that would have closed.

Two limits worth knowing: **a bot you ask cannot ask a third** (one
hop, by construction — chain work through the inbox instead), and **a
delegated question spends the asker's budget**, so ten questions divide
one budget rather than multiplying it.

## Routines

Ask a bot for one and it owns it:

> Every morning at 7, check the cluster and note anything odd.

That runs **as the devops bot** — its memory, its tools — rather than
as the assistant at large. The result lands in its inbox, so a week of
checks is something you can read rather than a week of notifications
you have learned to ignore.

## Getting told

When a bot needs to reach you it goes out on your normal channels,
labelled:

```
engineering: staging is live on v0.4.2
```

One Telegram bot and one Slack app serve the whole team, so a bot
created five seconds ago can already message you. The trade is a shared
avatar; the label is what tells them apart.

Reserve `notify` for things that need you now. The inbox is for
everything else.

## Teams

Bots belong to a team you name, and there can be several. Each team
has its own coordinator: the bot that answers when you message on
Telegram or Slack and hands work to the rest of that team. A bot
belongs to exactly one.

Existing deployments need no migration — a bot record with no team id
reads as the default team, so upgrading is one record appearing rather
than every record being rewritten.

See [the console](./console.md) for naming and switching them.

## Who answers on Telegram and Slack

The **coordinator of the default team**, not a separate assistant.

This was not true at first, and the gap was invisible from either
side: five code paths build a turn — Telegram, Slack, REST, the
inbound webhook, the console — and only the console named a bot. Every
other channel ran the pre-bot assistant, so you could build a team in
the browser, message Telegram, and reach somebody who had never heard
of them.

The resolution now happens in one place rather than at each channel,
so a channel added later cannot forget it.

You always talk to the coordinator; it delegates and the specialists
report back. There is no way to address a specialist directly from a
channel — one conversation to follow is the intended shape. In the
console you can talk to anyone directly.

## The web console

[Its own page](./console.md) — teams, the desk, receipts, approvals,
and signing in as more than one person.

:::warning Exposing it requires auth
The console can rewrite a bot's instructions, read every conversation
and assign the team work. Binding anything other than loopback **also
requires `[auth] require_auth = true`** — the node refuses to boot
otherwise.

That is deliberate: a warning in a log is not much use when the
consequence is somebody else's browser. Loopback is exempt, so running
it on your own machine needs no token.
:::

To sign in from anywhere else, give the console a token:

```toml
[gateway]
bind_address = "0.0.0.0"

[auth]
require_auth = true

[gateway.ui]
enabled   = true
token_ref = "env:LOBSLAW_CONSOLE_TOKEN"
```

You type that token once; the console keeps a 12-hour cookie. Enabling
`require_auth` **without** `token_ref` is refused at boot — it would be
a locked door with no key.

The console is compiled into the binary, but only if you build it:

```bash
make web && make build
```

A binary built without `make web` logs a warning and serves everything
else normally.

## Limits

- **The coordinator cannot be deleted.** It is what answers your messages.
  You can re-brief it.
- **A queued task cannot ask you to approve something.** Nobody is
  watching a task that drains at 3am, so it fails and says so. A turn
  you started — in the console, or by messaging a channel — *can* ask,
  and waits while you answer.
- **A queue has a depth limit** (200 unworked items by default). Past
  it, whoever is loading the bot is told — rather than the work being
  quietly dropped.
- **Disabling a bot** stops it working without losing its history.

## See also

- [Configuration reference](../configuration/reference.md) — `[bots]`
  and `[gateway.ui]`
- [Scheduler](./scheduler.md) — how routines fire
- [Notifications](./notifications.md) — where a bot's messages go
