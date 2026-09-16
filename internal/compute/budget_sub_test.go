package compute

import (
	"sync"
	"testing"
)

// The defect these tests pin: a fan-out that hands every child its
// own fresh TurnBudget bounds each child and bounds the run not at
// all. N children times a per-child cap is a spend multiplier, and
// before Sub existed no object anywhere held the total.
//
// Every assertion below COUNTS calls. None of them inspects
// configuration — a test that reads caps back out of a struct proves
// the struct was populated, not that anything is enforced.

func TestSubChildrenCannotCollectivelyExceedTheReservation(t *testing.T) {
	t.Parallel()
	const (
		reservationCap = 10
		children       = 8
		perChildCap    = 5 // 8 × 5 = 40 — four times the reservation
	)
	reservation, err := NewTurnBudget(BudgetCaps{MaxToolCalls: reservationCap})
	if err != nil {
		t.Fatalf("NewTurnBudget: %v", err)
	}

	allowed := 0
	for range children {
		child, err := reservation.Sub(BudgetCaps{MaxToolCalls: perChildCap})
		if err != nil {
			t.Fatalf("Sub: %v", err)
		}
		for range perChildCap {
			if child.RecordToolCall().Within {
				allowed++
			}
		}
	}

	if allowed != reservationCap {
		t.Errorf("children were allowed %d tool calls against a reservation of %d "+
			"(per-child cap %d × %d children = %d if nothing sums them)",
			allowed, reservationCap, perChildCap, children, perChildCap*children)
	}
}

func TestSubReportsTheReservationAsTheExceededBound(t *testing.T) {
	t.Parallel()
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: 1})
	// A child roomy enough that it would never object on its own.
	child, _ := reservation.Sub(BudgetCaps{MaxToolCalls: 100})

	if d := child.RecordToolCall(); !d.Within {
		t.Fatalf("first call should be within: %+v", d)
	}
	d := child.RecordToolCall()
	if !d.Exceeded {
		t.Fatalf("second call exceeded the reservation but the child reported within: %+v", d)
	}
	if d.ExceededOn != "tool_calls" {
		t.Errorf("ExceededOn = %q, want %q", d.ExceededOn, "tool_calls")
	}
	// The caller wants to know what IT spent, not what the whole
	// fan-out spent, so the child's own counters ride on the decision.
	if d.Current.ToolCalls != 2 {
		t.Errorf("Current.ToolCalls = %d, want the child's own count 2", d.Current.ToolCalls)
	}
}

func TestSubChildCapStillBoundsOneBadChild(t *testing.T) {
	t.Parallel()
	// The reservation is generous; the point is that one greedy child
	// cannot drain it before its siblings run.
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: 100})
	greedy, _ := reservation.Sub(BudgetCaps{MaxToolCalls: 3})

	allowed := 0
	for range 20 {
		if greedy.RecordToolCall().Within {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("greedy child spent %d, want its own share of 3", allowed)
	}
	// Only the three permitted calls reach the reservation. The
	// seventeen refusals never dispatched a tool, so charging the run
	// for them would let a greedy worker drain a budget by being
	// refused — which is the opposite of what refusing it is for.
	if drawn := reservation.State().ToolCalls; drawn != 3 {
		t.Errorf("reservation recorded %d draws, want 3 — refused calls must not draw", drawn)
	}
}

func TestSubSpendAndEgressAlsoDrawOnTheReservation(t *testing.T) {
	t.Parallel()
	reservation, _ := NewTurnBudget(BudgetCaps{MaxSpendUSD: 1.0, MaxEgressBytes: 100})
	a, _ := reservation.Sub(BudgetCaps{MaxSpendUSD: 1.0, MaxEgressBytes: 100})
	b, _ := reservation.Sub(BudgetCaps{MaxSpendUSD: 1.0, MaxEgressBytes: 100})

	if d := a.RecordCostUSD(CostRecord{CostUSD: 0.75}); !d.Within {
		t.Fatalf("first child within its own cap: %+v", d)
	}
	if d := b.RecordCostUSD(CostRecord{CostUSD: 0.75}); !d.Exceeded {
		t.Errorf("two children spent $1.50 against a $1.00 reservation without objection: %+v", d)
	}

	if d := a.RecordEgressBytes(60); !d.Within {
		t.Fatalf("first child within its own egress cap: %+v", d)
	}
	if d := b.RecordEgressBytes(60); !d.Exceeded {
		t.Errorf("two children egressed 120B against a 100B reservation without objection: %+v", d)
	}
}

func TestSubCheckConsultsTheReservation(t *testing.T) {
	t.Parallel()
	// Check is what the agent loop peeks with mid-turn. A child that
	// answers only for itself would report a comfortable "within"
	// while the run it belongs to is already over.
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: 1})
	quiet, _ := reservation.Sub(BudgetCaps{MaxToolCalls: 100})
	noisy, _ := reservation.Sub(BudgetCaps{MaxToolCalls: 100})

	noisy.RecordToolCall()
	noisy.RecordToolCall()

	if d := quiet.Check(); !d.Exceeded {
		t.Errorf("a sibling blew the reservation and Check still said within: %+v", d)
	}
}

func TestSubRelaxDoesNotLiftTheReservation(t *testing.T) {
	t.Parallel()
	// Approving one worker past its share must not become a way to
	// approve the whole fan-out past the bound every sibling shares.
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: 2})
	child, _ := reservation.Sub(BudgetCaps{MaxToolCalls: 1})
	child.Relax()

	allowed := 0
	for range 10 {
		if child.RecordToolCall().Within {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("relaxed child spent %d, want the reservation's 2", allowed)
	}
}

func TestSubIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	// 33b parallelises the workers. The reservation has to hold when
	// they draw at the same time, or the bound only exists in the
	// sequential case that is about to stop being the case.
	const (
		reservationCap = 50
		children       = 10
		perChild       = 20
	)
	reservation, _ := NewTurnBudget(BudgetCaps{MaxToolCalls: reservationCap})

	var (
		mu      sync.Mutex
		allowed int
		wg      sync.WaitGroup
	)
	for range children {
		child, _ := reservation.Sub(BudgetCaps{MaxToolCalls: perChild})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perChild {
				if child.RecordToolCall().Within {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if allowed != reservationCap {
		t.Errorf("concurrent children were allowed %d calls against a reservation of %d",
			allowed, reservationCap)
	}
}

func TestSubRejectsNegativeCaps(t *testing.T) {
	t.Parallel()
	reservation, _ := NewTurnBudget(BudgetCaps{})
	if _, err := reservation.Sub(BudgetCaps{MaxToolCalls: -1}); err == nil {
		t.Error("Sub accepted a negative cap; NewTurnBudget rejects one")
	}
}
