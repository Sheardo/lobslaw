---
sidebar_position: 4
---

# Notifications

Channel-agnostic message dispatch — the agent says `notify(text="...")` and the right channel(s) deliver.

## How it routes

```
agent / commitment / research                     ← notify(text="...", urgency="...")
            │
            ▼
       internal/notify/
            │
            ▼
  Routing decision:
  ├── inbound originator known?    ─► reply on that channel only
  └── self-generated (commitment, research, scheduled task)?
                                    ─► broadcast to every channel bound
                                       to the user (UserPrefs.Channels)
            │
            ▼
        Sink interface
        ├── TelegramSink    ─► sends via Telegram bot API
        ├── RESTSink         ─► returns error (REST is request/response)
        └── SlackSink etc.   ─► future
```

## The builtin

```
notify(
  text:         "...",
  user_id:      "owner",        # optional; defaults to current turn's user
  urgency:      "normal",       # low | normal | high | critical
  ttl_seconds:  300,             # default 5min for transient; 0 = no TTL
)
```

`text` is mandatory. Everything else is optional.

## Why channel-agnostic

The previous design had per-channel builtins (`notify_telegram`, future `notify_slack`, etc.). Two problems:

1. The agent had to know which channel to use. It often didn't.
2. Skills/commitments inherit the originating channel — there's no "telegram" or "slack" at that point, just "the user's preferred channel".

The new design: one builtin, one router, sinks decide how to deliver.

## TTL on transient notifications

If a channel is offline (Telegram bot mid-restart, REST listener bound but waiting), notifications expire after 5 min by default. The motivation: the agent's assumption was that the user would see it *now*. If the user didn't, surfacing it 6 hours later is worse than dropping it.

`ttl_seconds=0` disables TTL — useful for important notifications that should buffer until delivered.

## Bind a user to channels

```toml
[[user]]
id           = "owner"
display_name = "Alice"
scope        = "owner"
timezone     = "Europe/London"

[[user.channels]]
type    = "telegram"
address = "123456789"        # chat_id

[[user.channels]]
type    = "slack"
address = "U01234"            # user ID
```

When the agent calls `notify(user_id="owner", text="...")`:

- Find user prefs.
- Iterate channels.
- Dispatch via each sink.

A user bound to two channels gets both.

## Originator-aware routing

When a turn originates from a Telegram message:

- Inline replies (the agent's response to the turn) go to that Telegram chat — that's the gateway, not notify.
- Async / out-of-turn `notify(text="...")` calls during the turn route to that same Telegram chat — originator-aware.

When a turn originates from a commitment firing or research synth:

- There's no live channel.
- `notify` broadcasts to every channel bound to the commitment's `created_for` user.

## Urgency

| Urgency | Sink behaviour (Telegram) |
|---|---|
| `low` | Silent message |
| `normal` | Default delivery |
| `high` | Bold + mention |
| `critical` | Bold + mention + retry on failure |

Other sinks define their own mapping (Slack: thread vs. broadcast, color, retry policy).

## Sinks

A sink implements:

```go
type Sink interface {
    ChannelType() string
    Deliver(ctx context.Context, address, body string) error
}
```

Existing:

- `TelegramSink` — parses chat_id from address, sends via bot API
- `RESTSink` — always returns error (REST is request/response — there's no async push)

To add a new sink, implement the interface and register it from `wire_gateway.go`.

## Reference

- `internal/notify/notify.go` — service, routing, broadcast
- `internal/gateway/notify_sinks.go` — TelegramSink, RESTSink
- `internal/compute/builtin_notify.go` — agent-facing builtin
- `internal/memory/user_prefs.go` — user → channel binding lookup

## Who sent it, and who asked

Two different facts, both stamped from the turn and never from a tool
argument — a sender or requester the model can name is one it can
invent.

**Who sent it** is the bot. On a channel that can show it, Slack does:
the message arrives under that bot's own display name and icon. On one
that cannot, it is a prefix — `DevOps: deploy finished`.

**Who asked** is the person the work traces back to, and it appears
only when that is somebody other than the reader:

```
Weekly report: all queues clear, 4 bots active.

(Sam asked me to send you this)
```

This matters as soon as more than one person can talk to the same
team. "Prepare a report and send it to James" is an ordinary request,
and without provenance James receives a message from a bot with no way
to tell whether he asked for it, a colleague did, or it arrived
unprompted. The last reading is the dangerous one: anybody able to
talk to a bot could otherwise make it send messages that read as
though the system originated them.

### It survives delegation

The requester is carried on the queue item, so it outlives the hop
that would otherwise destroy it:

```
Sam → coordinator          turn runs as Sam, requester = Sam
  → inbox_post to research item records requested_by = user:sam
    → research works it    turn runs as bot:research, requester still Sam
      → notify james       "(Sam asked me to send you this)"
```

`sender` on a queue item says who *put it there*, which after one hop
is another bot. `requested_by` stays the human at the start of the
chain.

Nothing is added when nobody asked — a routine firing at 3am has no
requester, and inventing one would be worse than having none.
