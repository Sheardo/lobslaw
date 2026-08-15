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

Each is a question about a harness, answerable by exporting what the
harness already knows.
