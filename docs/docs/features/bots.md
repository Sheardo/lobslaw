---
sidebar_position: 1
---

# Bots

lobslaw starts as one assistant. You can give it colleagues.

The one you already talk to becomes the **chief of staff**, keeping the
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

## The web console

```toml
[gateway]
bind_address = "127.0.0.1"

[gateway.ui]
enabled = true
```

Then open `http://127.0.0.1:8080/`. It shows the whole team on one
page: what each bot is working on, what it finished, what failed and
why. Assign work, retry something, edit a brief, or chat to **any**
bot — not just the chief.

Open a finished item and you can read the turn that produced it, not
only its result.

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

- **The chief cannot be deleted.** It is what answers your messages.
  You can re-brief it.
- **A bot cannot ask you to approve something mid-delegation.** A task
  needing a confirmation fails and says so; ask the bot directly and
  approve it there.
- **A queue has a depth limit** (200 unworked items by default). Past
  it, whoever is loading the bot is told — rather than the work being
  quietly dropped.
- **Disabling a bot** stops it working without losing its history.

## See also

- [Configuration reference](../configuration/reference.md) — `[bots]`
  and `[gateway.ui]`
- [Scheduler](./scheduler.md) — how routines fire
- [Notifications](./notifications.md) — where a bot's messages go
