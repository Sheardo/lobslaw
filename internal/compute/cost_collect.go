package compute

import (
	"context"
	"sync"
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
