# lobslaw — Turn tracing and cost attribution

Design for per-turn timing, token and cost telemetry: what a turn
spent, where, and which tool caused it.

lobslaw is a harness. This data is **exported** — OpenTelemetry, a
debug file, a webhook — not reported inside the assistant. Nothing here
adds a `lobslaw trace report` command or a raft-backed trace store.
Traces are high-volume diagnostic output with a short useful life; raft
is for state the cluster must agree on, and a trace is neither
agreed-upon nor state.

---

## What is already computed, and thrown away

Per LLM round-trip the agent already builds:

```go
type CostRecord struct {
	ProviderLabel string
	Model         string
	Usage         Usage    // prompt / completion / cached / total tokens
	CostUSD       float64
}
```

…hands it to `TurnBudget.RecordCostUSD`, which folds it into running
totals and **discards the record**. `BudgetState` survives; the
per-call detail does not.

`ToolInvocation` carries `CallID`, `ToolName`, `Args`, `Output`,
`ExitCode`, `Error` — and no timing, no size, no cost.

So the interesting data is produced and dropped on the floor. This work
is mostly *retaining and emitting* what exists, plus one genuinely new
calculation (below).

---

## The span model

A turn emits an ordered tree of spans.

```
Span {
  turn_id      string
  span_id      string
  parent_id    string        // tool calls nest under the llm_call that requested them
  kind         llm_call | tool_call | embedding | retrieval | compaction | ingest
  name         model name, or tool name
  provider     provider label
  started_at   time
  duration     time.Duration
  usage        prompt / completion / cached tokens   (llm_call, embedding)
  cost_usd     float64
  result_bytes int                                    (tool_call)
  result_tokens int                                   (tool_call)
  error        string
}
```

The parent link matters: it makes "which model call asked for this
tool" answerable, which is what turns a flat list of timings into
something you can reason about when a turn took 40 seconds.

`kind` deliberately includes the non-LLM work — retrieval, compaction,
episodic ingest. A turn that felt slow is often slow in the parts
nobody instruments, and a trace that only shows model calls quietly
implies the rest was free.

---

## Attributing context cost to tools

This is the part worth building carefully, because the obvious version
is wrong.

A tool's cost is **not** the call that ran it. It is the tokens its
output contributed to every subsequent prompt in the turn. A tool that
returns 8k tokens on the first of six model calls is not an 8k-token
event — it is carried in five later prompts, so it costs roughly 40k
*prompt* tokens, priced at the prompt rate.

Nothing surfaces that today, and it is usually the dominant cost in an
agentic turn. An operator looking at a bill sees "prompt tokens: large"
with no way to attribute it.

So:

- record `result_tokens` on each `tool_call` span;
- record, on each `llm_call` span, which tool-result spans were present
  in its prompt;
- attribution for a tool = `result_tokens × (number of later llm_calls
  carrying it) × prompt price`.

Both inputs are already available at the point the prompt is
assembled — the agent builds the message list, so it knows exactly
which tool results are in it. The count is bookkeeping, not
estimation.

### Cost is not always denominated in tokens

`Usage{PromptTokens, CompletionTokens, TotalTokens, CachedTokens}` and
a per-token price describe chat and embedding. They do not describe the
generation modalities, which bill per image, per megapixel, per second
of video, per second of GPU, or in credits against a plan — see
[PROVIDERS](/dev/PROVIDERS#billing-is-not-tokens).

Multiplying a zero token count by a per-token price yields **zero**,
and a cost report stating that a video generation was free is worse
than one that omits it. It is a confidently wrong number, and nothing
about it invites doubt.

So a span records a **unit and a quantity** — `tokens`, `images`,
`megapixels`, `video_seconds`, `gpu_seconds`, `credits` — with token
detail as one case rather than the only case. Where the provider bills
against a plan quota rather than a balance, the span records the
credits consumed and marks `billed_to: plan`, because the marginal USD
really is zero and the meaningful number is the quota drawn down.

An operator on a plan wants to know how fast they are consuming it, and
a spend figure of £0 answers a question they did not ask.

Two honesty constraints:

- **Token counts must be measured, not guessed.** Where the provider
  returns usage, use it. Where a tool result's token count has to be
  computed locally, it is an approximation and the field says so —
  `result_tokens_estimated: true` — rather than presenting a tokeniser
  guess as fact.
- **Cached tokens are already in `Usage`** and must not be priced as
  fresh. A provider that caches the prompt prefix changes the answer
  substantially, and ignoring it would overstate the cost of exactly
  the long-context turns this exists to explain.

---

## Sinks

Emission is pluggable and off by default.

```toml
[trace]
enabled = false

# Any combination.
[trace.otel]
endpoint = "http://localhost:4317"

[trace.file]
path = "/var/log/lobslaw/turns.jsonl"

[trace.webhook]
url        = "https://…/lobslaw-turns"
secret_ref = "env:TRACE_WEBHOOK_SECRET"
```

**OpenTelemetry is the primary sink** and the span model above is
shaped for it deliberately — `turn_id` as the trace, spans nesting
naturally, tokens and cost as attributes. That buys every existing
tracing backend for free rather than inventing a viewer.

**File** is newline-delimited JSON, one span per line. The sink that
works when nothing else is running, and the one to reach for when
debugging a single misbehaving turn.

**Webhook** posts a completed turn's spans as one document. For
routing into whatever the operator already runs.

Three rules for all of them:

- **Never on the reply path.** Emission is asynchronous and bounded;
  a slow collector must not slow a turn or fail one. A dropped span is
  strictly better than a delayed answer, and the drop is counted.
- **No content.** Spans carry sizes, counts, timings and names — never
  message text, tool arguments or tool output. A trace pipeline is
  usually less protected than the store, and a turn's content is the
  most sensitive thing in the system. `Args` and `Output` exist on
  `ToolInvocation`; they do not belong in a span.
- **Off by default**, because a harness that ships with telemetry on is
  making a decision that is not its to make.

---

## Relationship to the audit chain

lobslaw already has a hash-chained audit log (`BucketAuditEntries`,
`AuditService.Append` / `Query` / `VerifyChain`). Traces are not audit
entries and the two should not merge:

| | Audit | Trace |
|---|---|---|
| Question | who did what, provably | where did the time and money go |
| Integrity | hash-chained, verifiable | best-effort, lossy by design |
| Retention | long, operator-defined | short |
| Storage | raft | exported off-node |
| On failure | must not be lost | dropped and counted |

An audit entry that could be dropped under load is not an audit entry.
A trace that must never be dropped becomes a reliability problem on the
reply path. Different guarantees, so different pipes.

---

## Why this is worth doing

Three questions an operator cannot answer today:

1. *Why did that turn take 40 seconds?* — no per-span timing, and the
   non-LLM work is entirely invisible.
2. *What is actually costing me money?* — totals only, no attribution,
   and the largest contributor (tool output re-sent across a tool loop)
   is not measured anywhere.
3. *Is my primary provider being used?* — the failover chain walks
   silently; the trace names the provider that answered.
4. *How fast am I burning my plan quota?* — plan-billed providers block
   outright when exhausted rather than charging overage, so the useful
   signal is consumption rate, and no spend figure conveys it.

Each is a question about a harness, answerable by exporting what the
harness already knows.

---

## What has landed

The span model, the recorder, the local file sink, OTLP export,
`lobslaw trace`, and provider-attempt instrumentation. The webhook
sink and the non-LLM spans (retrieval, compaction, ingest) are not
built yet, nor is tool context attribution.

### Stored locally, contrary to the design above

This document says *"exported, not stored — no raft bucket and no
reporting command"*. That argument was right about **raft**: a trace is
high-volume, short-lived, and not agreed-upon state, so replicating it
would drag telemetry into the consensus path.

A per-node file is not raft. It gives `lobslaw trace <turn-id>` without
any of that, and it means the record survives a collector being down —
which is when you most want it.

The honest cost: **a turn served on node A is not queryable from node
B.** The trace is local because the turn was, and the CLI says so when
it finds nothing.

```
<data-dir>/traces/turns.ndjson     current
<data-dir>/traces/turns.ndjson.1   one predecessor
```

Bounded and rotated, because an unbounded telemetry file on a
long-running node is a disk-full incident waiting for a quiet week. Two
files is a ceiling somebody can reason about; a numbered series is the
same problem with extra steps. `ReadTurn` reads both, so a turn that
straddles a rotation comes back whole.

### Dropping is the correct behaviour

`Record` never blocks. A full buffer drops the span and counts the
drop.

That is not a compromise — tracing must never slow or fail a turn, and
a collector that hangs, a disk that fills or a sink that errors must
not reach the user waiting for a reply. The **count** is what makes it
honest: a trace with a hole that says "4 dropped" is usable, while one
that silently omits the interesting span is worse than no trace,
because it is read as evidence of absence.

This is also why traces stay away from the hash-chained audit log. An
audit entry that may be dropped under load is not an audit entry.

### Every attempt, not just the winner

A provider span is emitted for the winner, for each candidate that
failed and advanced, and for each candidate **never tried** — demoted
by health, or refused by the trust floor.

The skipped ones matter most. "The chain skipped three providers before
succeeding" is the shape of a developing outage, and it is invisible if
only attempts are recorded. `outcome` distinguishes them, because
counting a protective decision as a failure would make the trust floor
look like an outage.

### The cost was never computed

This document says the per-round-trip cost record is *"computed and
then discarded"*. It was not computed.

`CostRecord` was built with `ProviderLabel: ""` and `CostUSD: 0`,
behind a comment saying a later phase would fill it in.
`dispatchWithBackup` did not return which provider had won, so the
caller had nothing to price against. **Every turn to date reported a
spend of nothing** — which also means the budget's spend cap has never
fired.

The winning entry now comes back from dispatch carrying its model and
pricing, and the cost is attributed to the provider that actually
served the turn rather than to the one that was asked. On a failover
those differ, and attributing to the requested model would misprice
exactly the turns worth auditing.

---

## OTLP export

```toml
[trace]
enabled       = true
otlp_endpoint = "localhost:4317"
otlp_insecure = true          # named for what it does
service_name  = "lobslaw"
```

**In addition to the file, not instead of it.** The file is the
record; the collector is where you look. A collector going down must
not lose the trace of the turn that was failing while it was down —
which is exactly the trace anybody would want afterwards.

### Written against the wire format, not the SDK

The OpenTelemetry Go SDK brings a tracer provider, a span processor, a
batcher and a context-propagation layer. This package already has its
own version of every one of them. Adopting the SDK would mean
converting our spans into its spans so that its batcher could hand
them to its exporter — a lot of machinery to serialise a struct we
already own.

So the exporter is a `Sink` like any other. The recorder's
non-blocking dispatch and drop accounting apply unchanged, which is
the property that matters.

Cost: `go.opentelemetry.io/proto/otlp`, which drags in
`grpc-gateway/v2` because the generated REST-gateway file lives in the
same package as the request message. That is the standard cost of
OTLP/gRPC in Go — the official exporter takes it too.

### Ids are hashed, not generated

OTLP wants 16 bytes of trace id and 8 of span id; ours are ULIDs.
Hashing is deterministic, so the same turn produces the same trace id
on every node and in every export — which is what makes a re-export
idempotent and a turn's spans group into one trace rather than
scattering across three.

Trace and span ids use **different hash prefixes**, so a turn id and a
span id that happened to be equal cannot produce the same bytes. That
would make a span its own parent, and it is the kind of thing that
surfaces as one inexplicable trace six months later.

### Three failure modes, each handled

**A collector that hangs.** Every export carries a deadline. Without
one the exporter blocks forever, the recorder's single background
goroutine never returns, and every span is dropped for the life of the
process — *including the ones destined for the local file*, which has
nothing wrong with it.

**A collector that refuses.** The batch is dropped, not requeued. A
collector that is down stays down for minutes, and a queue that grows
for the duration is how a telemetry outage becomes a memory incident.
The recorder counts the failure.

**A collector that is down at boot.** gRPC dialling is lazy, so the
node starts and the collector picks up when it returns. A failure
constructing the exporter is logged and tracing degrades to local-only
rather than refusing to boot — taking the assistant down to protect
telemetry is the wrong trade.

### Status codes

A failed attempt is `ERROR` so a collector's own filters find it. A
**skipped** candidate is `UNSET`: it did not fail, it was never tried,
and colouring a protective decision red is how a working trust floor
gets reported as an outage.
