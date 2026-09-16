# Bots — giving yourself a team

lobslaw starts as one assistant. You can give it colleagues.

The one you already talk to becomes the **coordinator**. It keeps
the personality and the memory it already had — nothing about your
first conversation after upgrading is different. What changes is that
it can now create specialists, hand them work, and tell you what they
did.

---

## Making one

Ask, on whatever channel you normally use:

> Make me a devops bot. It watches the Kubernetes cluster, and it
> should use shell_command and read_file but nothing else.

Or open the console (see below) and fill in the form. Either way you
end up with a bot that has:

- **A brief.** "You are the engineer for XYZ, you own the build and the
  deploy pipeline." This is standing configuration, not a task — write
  it about the role.
- **Its own memory.** What the marketing bot learns does not show up in
  the devops bot's recall.
- **Its own tools.** A tool you leave out is one the bot is never even
  shown, so this is how you decide what it can do. Leave the list empty
  and it gets everything the node has.
- **Its own inbox.** See below — this is most of the point.

---

## The inbox

Every bot has a durable queue. You put work in it; the bot pulls the
next item, does it, and writes the outcome back.

That matters more than it sounds:

- **Work survives.** Restart the node, lose a leader, disable the bot
  and re-enable it — the task is still there.
- **Results have somewhere to live.** "Deploy staging" comes back with
  what version went live, attached to the task you asked about.
- **Nothing disappears.** A task that fails is retried, and when it
  runs out of retries it sits there marked *failed* with the reason on
  it. There is a retry button.

Bots use it on each other too. Marketing can hand engineering a task
and get the result back later, or — when it needs the answer to carry
on writing — ask a question and wait for it.

You grant those connections explicitly:

> Let marketing ask engineering things.

Loops are refused when you set them up, not when they happen, and the
error tells you which path would have closed.

---

## Routines

Ask a bot for one and it owns it:

> Every morning at 7, check the cluster and note anything odd.

That runs **as the devops bot** — its memory, its tools — rather than
as the assistant at large. The result lands in its inbox, so a week of
checks is something you can read rather than a week of notifications
you have learned to ignore.

Use `notify` for things that genuinely need you now. The inbox is for
everything else.

---

## Getting told

When a bot does need to reach you it goes out on your normal channels,
labelled with who sent it:

```
engineering: staging is live on v0.4.2
```

One Telegram bot and one Slack app serve the whole team, so a bot you
created five seconds ago can already message you. The trade is that
they share an avatar — the label is what tells them apart.

---

## The web console

Switch it on:

```toml
[gateway]
bind_address = "127.0.0.1"

[gateway.ui]
enabled = true
```

Then open `http://127.0.0.1:8080/`.

It gives you the whole team on one page: what each bot is working on,
what it finished, what failed and why. You can assign work, retry
something, edit a brief, or chat to the coordinator.

### Signing in from anywhere but this machine

```toml
[gateway]
bind_address = "0.0.0.0"

[auth]
require_auth = true

[gateway.ui]
enabled   = true
token_ref = "env:LOBSLAW_CONSOLE_TOKEN"
```

You type that token once and the console keeps a 12-hour cookie. On
loopback you need none of this.

### It will refuse to start if you expose it without auth

The console can rewrite a bot's instructions, read every conversation
and assign the team work. If you bind anything other than loopback you
must also set:

```toml
[auth]
require_auth = true
```

The node refuses to boot otherwise. That is deliberate — a warning in a
log is not much use when the consequence is somebody else's browser.
Binding `127.0.0.1` is exempt, so running it on your own machine needs
no token.

It also refuses `require_auth` with no `token_ref`: that combination
demands a credential nobody can present.

### Building it

The console is compiled into the binary, but only if you build it:

```bash
make web && make build
```

A binary built without `make web` logs a warning and serves everything
else normally. Nothing else breaks.

---

## Limits worth knowing

- **A bot you ask cannot ask a third.** One hop, by construction. If
  you need a chain, hand work over with the inbox instead.
- **A delegated question spends the asker's budget.** Ten questions do
  not cost ten budgets — they divide one.
- **A bot cannot ask you to approve something mid-delegation.** If a
  task needs a confirmation, it fails and says so; ask the bot directly
  and approve it there.
- **The coordinator cannot be deleted.** It is what answers your messages.
  You can re-brief it.
- **A queue has a depth limit** (200 unworked items by default). Past
  it, whoever is loading the bot gets told — rather than the work being
  quietly dropped.

---

## Where things are

| You want | Look at |
|---|---|
| Config keys | [configuration reference](../docs/configuration/reference.md) — `[bots]`, `[gateway.ui]` |
| How it works inside | [docs/dev/BOTS.md](../dev/BOTS.md) |
| Channel setup | [CHANNELS.md](CHANNELS.md) |
