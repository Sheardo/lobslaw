---
sidebar_position: 2
---

# The web console

A browser view of your team: who works for you, what they are doing,
what they have done, and what needs a decision from you.

It is off by default. It is an admin surface — it can rewrite what
every bot does — and one you have not asked for should not be
listening.

## Turning it on

```toml
[gateway]
enabled      = true
http_port    = 8443
bind_address = "127.0.0.1"

[gateway.ui]
enabled   = true
token_ref = "env:LOBSLAW_CONSOLE_TOKEN"
```

Then open `http://127.0.0.1:8443/`.

`lobslaw init` writes both blocks commented, so a fresh install only
has to flip `enabled` and uncomment `token_ref`.

:::warning Non-loopback needs authentication
Changing `bind_address` away from loopback forces
`[auth] require_auth = true`. The node refuses to start otherwise,
rather than warning — an unauthenticated admin console reachable from
the network is not a configuration to warn about.
:::

The console is compiled into the binary, but only if you build it:

```bash
make web && make build
```

## Teams

Bots belong to a **team**, and you name it. Click the title on the
desk and type; the switcher above the roster moves between teams and
creates new ones.

Each team has its own **coordinator** — the bot that answers when you
message on Telegram or Slack, and the one that hands work to everybody
else on that team. That is why teams exist rather than tags: "who
replies when I message" needs an answer per team, not one answer
globally.

A bot is in exactly one team. A bot with two managers has two sets of
instructions about what matters, and the point of the record is that
there is one answer to that. Move a bot from its Settings panel; the
coordinator is the exception and cannot be moved, because it is what
inbound messages reach.

Upgrading needs no migration. A bot record written before teams
existed has no team id, which reads as the default team — so the
upgrade is one record appearing, not every record being rewritten.

## The desk

The landing view, in three parts.

**Needs you.** Anything that failed, pulled to the top with the error
and a retry. It is the only thing on the page still drawn with an
edge, because it is the only thing that should interrupt you.

**The team.** Everyone, with their remit, whether they are working or
idle, and who they are allowed to brief. That last one is read from
the same message graph that *authorises* the messaging, so the picture
cannot disagree with the permissions.

**Recent work.** What the team has been doing, newest first. An entry
marked `← Coordinator` is work one bot handed to another — the
coordinator breaking your ask into pieces and giving them out.

## Talking to a bot

Click anyone to open their room. Replies stream in as they are
written; a turn against a real model routinely takes thirty to a
hundred seconds, so the indicator names who is working and counts
elapsed time rather than animating silently.

A bot's own thread interleaves three things in the order they
happened: what you said, work in its queue, and work it handed to
somebody else. Following a handoff is what makes delegation visible
where it occurred rather than only in the recipient's queue.

### Receipts

Under every reply is what that turn **actually ran** — the tools it
invoked, the tokens it used, and the cost.

This is not decoration. A bot's account of its own work is a claim,
and the two are not always the same: a coordinator turn once reported
setting a reminder it had never set, and nothing in the system
contradicted it. The reply is the claim; the receipt is the record.

### Approvals

When a turn needs permission — a write, a shell command, anything your
`approval_mode` does not cover — the question appears **in the
console** with Approve and Deny, and the turn waits on the open
connection while you decide.

A prompt channel that fails is an unanswered question, never consent:
if the prompt cannot be raised the turn stops and says so.

### Settings, routines and memory

Each bot's Settings panel holds its brief, its tool allowlist, who it
may message — and two read-only views:

- **Routines** — what it has scheduled for itself. This list earns its
  place mainly by making an *absence* visible: a routine that was
  never created looks exactly like one that was, until you have
  somewhere to look.
- **What it remembers** — read-only on purpose. The question it
  answers is "why did it say that", and a viewer that also let you
  rewrite the evidence would be a worse answer to it.

## From your phone

The console is built for a phone as well as a desktop: the sidebar
becomes a drawer, the layout goes single-column, and the composer
respects the home-bar inset.

To reach it from another device, bind to the network and require
authentication:

```toml
[gateway]
bind_address = "0.0.0.0"

[auth]
require_auth = true
```

On WSL2 you will also need a port forward from Windows, because WSL
sits behind NAT:

```powershell
netsh interface portproxy add v4tov4 listenport=8443 listenaddress=0.0.0.0 `
  connectport=8443 connectaddress=<your-wsl-ip>
New-NetFirewallRule -DisplayName "lobslaw console" -Direction Inbound `
  -Action Allow -Protocol TCP -LocalPort 8443
```

The WSL address changes when WSL restarts. `networkingMode=mirrored`
in `.wslconfig` removes the need for the forward entirely.

## More than one person

Give each person their own login in `[[user]]`:

```toml
[[user]]
id                = "james"
display_name      = "James"
roles             = ["operator"]
console_token_ref = "env:JAMES_CONSOLE_TOKEN"
```

Their session then carries *them* — the principal `user:james`, plus
the roles you declared — so the policy engine decides against who is
asking, and the console shows who is signed in. Those roles were
always declarable here and previously had nowhere to apply, because
every console session was one anonymous subject.

The principal form matters: a console session and a Telegram message
from the same person resolve to one identity, so their memory and
their work follow them between the two.

A person **without** `console_token_ref` cannot sign in. That is
deliberate — `[[user]]` is also where a Telegram chat id is bound, and
binding a chat must not hand out a login that can rewrite what every
bot does. The single shared `[gateway.ui] token_ref` still works and
stays anonymous.

## Conversations persist

A bot's thread is durable. It survives a refresh, a restart and a
leader change, and the **bot** sees it too — a follow-up question
lands with the conversation behind it rather than as a fresh start.

Stored messages carry a sequence number and no timestamp, so the
console anchors them to when the session was last written and spaces
them backwards. Their order relative to each other is exact; their
position among queue items is approximate.

History replayed into a turn is capped (40 messages, with the running
summary standing in for anything older) because the context budget is
finite: an unbounded replay would let the oldest message in a long
thread quietly evict the system prompt.

## Asking a bot to message somebody else

With more than one person signed in, "prepare a report and send it to
James" works — and James is told who asked:

```
Weekly report: all queues clear, 4 bots active.

(Sam asked me to send you this)
```

See [notifications](./notifications.md#who-sent-it-and-who-asked) for
how that survives being handed between bots.

Note that a bot may decline. A coordinator briefed to flag
unverifiable claims will refuse to send one and say why, rather than
passing it on — which is the behaviour you asked for, arriving at a
moment you may not expect it.

## What it does not do yet

- **No editing memory**, by design, as above.
- **No per-person permissions beyond roles.** Anyone who can sign in
  can act; what a role may do is whatever policy you wrote for it.
