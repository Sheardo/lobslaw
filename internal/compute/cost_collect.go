package compute

import (
	"context"
	"sync"
	"time"

	"github.com/jmylchreest/lobslaw/internal/ids"
	"github.com/jmylchreest/lobslaw/internal/trace"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// What a generation cost, on its way back to the turn's budget.
//
// Video, image and speech generation reported NOTHING. Not an
// approximation, not a zero with a note — no cost record at all, so a
// turn that rendered a minute of video spent, as far as the budget was
// concerned, nothing. The spend cap could not fire on it and the trace
// could not show it.
//
// The shape follows ArtifactCollector deliberately. A builtin has no
// reference to the TurnBudget and should not: it would then be able to
// refuse a turn on the budget's behalf, halfway through, from inside a
// tool. Announcing what was spent and letting the loop decide keeps
// the decision in one place.

type costCollectorKey struct{}

// CostCollector gathers the costs incurred during one turn.
type CostCollector struct {
	mu      sync.Mutex
	records []CostRecord
}

// WithCostCollector attaches a fresh collector to ctx. The agent calls
// this once per turn and drains it into the budget.
func WithCostCollector(ctx context.Context) (context.Context, *CostCollector) {
	c := &CostCollector{}
	return context.WithValue(ctx, costCollectorKey{}, c), c
}

// CollectCost announces a spend.
//
// A no-op when no collector is present — builtins are called from
// tests, from the scheduler and from turns with no budget attached,
// and none of those should have to care.
func CollectCost(ctx context.Context, rec CostRecord) {
	c, ok := ctx.Value(costCollectorKey{}).(*CostCollector)
	if !ok || c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}

// Drain returns what has been collected and empties the collector.
//
// Drained rather than read, because the agent takes it inside the tool
// loop: a turn that generates two videos across two round-trips must
// not bill the first one twice.
func (c *CostCollector) Drain() []CostRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) == 0 {
		return nil
	}
	out := make([]CostRecord, len(c.records))
	copy(out, c.records)
	c.records = c.records[:0]
	return out
}

// CollectGeneration records what a generation cost and emits its span.
//
// ONE CALL DOING BOTH, deliberately. The budget and the trace are two
// accounts of the same spend, and two call sites per modality is how
// they come to disagree — one modality gets a span and no cost record,
// another the reverse, and nobody notices until the numbers are
// compared.
//
// A failed generation records nothing against the budget: the provider
// did not deliver, and charging for it would make an outage look like
// usage. The SPAN is still emitted, because an attempt that failed is
// exactly what somebody reading a trace is looking for.
func CollectGeneration(
	ctx context.Context,
	provider, model string,
	usage ModalUsage,
	pricing types.ProviderPricing,
	started time.Time,
	err error,
) {
	rec := RecordModalCost(provider, model, usage, pricing)

	if err == nil {
		CollectCost(ctx, rec)
	}

	rec2, turnID := trace.FromContext(ctx)
	span := trace.Span{
		TurnID:    turnID,
		SpanID:    ids.New(),
		Kind:      trace.KindGeneration,
		Provider:  provider,
		Name:      model,
		StartedAt: started,
		Duration:  time.Since(started),
		Outcome:   trace.OutcomeOK,
		// The unit and quantity travel even when the cost is zero. A
		// plan-billed call has no marginal cost, and a span reporting
		// only £0 is indistinguishable from one that did nothing.
		Unit:     string(usage.Unit),
		Quantity: usage.Quantity,
		BilledTo: string(usage.BilledTo),
		CostUSD:  rec.CostUSD,
	}
	if err != nil {
		// Aborted rather than advanced: a generation builtin that
		// failed has exhausted its own failover chain by the time it
		// returns, so nothing further will be tried for this call.
		span.Outcome = trace.OutcomeAborted
		span.Error = err.Error()
		// A failure is not a charge, so the span says what was
		// attempted and costs nothing.
		span.CostUSD = 0
	}
	rec2.Record(span)
}
