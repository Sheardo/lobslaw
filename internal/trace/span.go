// Package trace records what a turn did and what it cost.
//
// Turn tracing: what a turn actually did, and what it cost.
//
// Three questions an operator cannot answer without it. Why did that
// turn take forty seconds — there is no per-span timing, and the
// non-LLM work is entirely invisible. What is costing money — the
// per-round-trip cost record is computed and then discarded into
// budget totals. Is the primary provider being used — failover walks
// silently.
//
// NO SPAN CARRIES CONTENT. Not message text, not tool arguments, not
// tool output. Sizes, counts, timings, names and labels only. This is
// not a convention to be relaxed later: a trace goes to whatever
// telemetry the operator already runs, which is a different trust
// boundary from the conversation, and a span that carried a prompt
// would move the conversation across it silently.
package trace

import (
	"time"
)

// Kind is what a span measures.
//
// Deliberately includes the non-LLM work. A turn that felt slow is
// often slow in the parts nobody instruments, and a trace showing only
// model calls quietly implies the rest was free.
type Kind string

const (
	KindLLMCall    Kind = "llm_call"
	KindToolCall   Kind = "tool_call"
	KindEmbedding  Kind = "embedding"
	KindRetrieval  Kind = "retrieval"
	KindCompaction Kind = "compaction"
	KindIngest     Kind = "ingest"
)

// Outcome is how an attempt ended. Named rather than derived from a
// non-empty error string, because "advanced to the next provider" and
// "gave up" are different facts and both have an error attached.
type Outcome string

const (
	OutcomeOK Outcome = "ok"
	// OutcomeAdvanced is a failure that moved to the next candidate.
	// The turn may still have succeeded; this span did not.
	OutcomeAdvanced Outcome = "advanced"
	// OutcomeAborted is a failure that stopped the chain.
	OutcomeAborted Outcome = "aborted"
	// OutcomeSkipped is a candidate never tried — demoted by health,
	// or refused by the trust floor. It has no duration, and counting
	// it as a failure would make a protective decision look like an
	// outage.
	OutcomeSkipped Outcome = "skipped"
)

// Usage is token accounting for a call billed in tokens.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	// CachedTokens is the prompt subset served from cache. Recorded
	// separately because it is priced differently, and folding it into
	// PromptTokens would overstate the cost of every cached turn.
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// Span is one measured piece of a turn.
type Span struct {
	TurnID   string `json:"turn_id"`
	SpanID   string `json:"span_id"`
	ParentID string `json:"parent_id,omitempty"`
	Kind     Kind   `json:"kind"`

	// Name is the model or tool name. A name, never an argument.
	Name string `json:"name,omitempty"`
	// Provider is the configured provider label.
	Provider string `json:"provider,omitempty"`

	StartedAt  time.Time     `json:"started_at"`
	Duration   time.Duration `json:"duration_ns,omitempty"`
	Outcome    Outcome       `json:"outcome"`
	Usage      Usage         `json:"usage,omitzero"`
	CostUSD    float64       `json:"cost_usd,omitempty"`
	ResultSize int           `json:"result_bytes,omitempty"`

	// Unit and Quantity carry billing that is not denominated in
	// tokens — video seconds, images, credits. A zero token count on a
	// call that cost real money reads as a free call, which is worse
	// than no number at all.
	Unit     string  `json:"unit,omitempty"`
	Quantity float64 `json:"quantity,omitempty"`

	// Error is the failure text, which for a provider error is a
	// status and a classification rather than a response body. Callers
	// pass an already-safe string; nothing here redacts, because a
	// redactor is a thing people trust and then feed a payload to.
	Error string `json:"error,omitempty"`

	// Attempt is the position in a failover chain, 0-based. It is what
	// makes "my primary is never used" answerable.
	Attempt int `json:"attempt,omitempty"`
}

// SkippedSpan builds the record for a candidate that was never tried.
//
// A span rather than a log line, because "the chain skipped three
// providers before succeeding" is the shape of a developing outage and
// it is invisible if only attempts are recorded.
func SkippedSpan(turnID, spanID, provider, reason string, attempt int) Span {
	return Span{
		TurnID:   turnID,
		SpanID:   spanID,
		Kind:     KindLLMCall,
		Provider: provider,
		Outcome:  OutcomeSkipped,
		Error:    reason,
		Attempt:  attempt,
	}
}
