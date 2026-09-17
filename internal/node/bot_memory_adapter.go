package node

import (
	"context"
	"sort"

	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/internal/memory"
)

// botMemoryAdapter exposes a principal's own memory to the console,
// read-only.
//
// Ownership is already a first-class property of every record, so this
// is a view over the existing query rather than a new store: a bot's
// memory is simply the records owned by bot:<id>. The console needed
// it because "why did it say that" was unanswerable — the reasoning
// was gone and the only surviving trace of what a bot knew was the
// reply itself.
type botMemoryAdapter struct{ store *memory.Store }

// RecordsForOwner returns the newest records a principal owns, plus
// the pre-limit total so the caller can say how much it is not showing.
func (a *botMemoryAdapter) RecordsForOwner(_ context.Context, owner string, limit int) ([]gateway.MemoryRecordView, int, error) {
	if a == nil || a.store == nil {
		return nil, 0, nil
	}
	page, err := memory.QueryRecords(a.store, memory.RecordFilter{
		Kind:  memory.KindEpisodic,
		Owner: owner,
	})
	if err != nil {
		return nil, 0, err
	}

	out := make([]gateway.MemoryRecordView, 0, len(page.Episodics))
	for _, e := range page.Episodics {
		row := gateway.MemoryRecordView{
			ID:   e.GetId(),
			Kind: "episodic",
			// Event is the thing itself; context is why it mattered.
			// Both, because an event without its context frequently
			// reads as a non-sequitur.
			Text: e.GetEvent(),
			Tags: e.GetTags(),
		}
		if c := e.GetContext(); c != "" {
			row.Text += " — " + c
		}
		if ts := e.GetTimestamp(); ts != nil {
			row.CreatedAt = ts.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		out = append(out, row)
	}

	// Newest first: recency is what makes a memory list answer
	// "why did it just say that".
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })

	total := len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}
