package agentcontrol

import "testing"

func TestWaitBudgetObserveCountsConsecutiveNoProgressWaits(t *testing.T) {
	budget := &WaitBudget{}
	key := "parent|turn-1"

	if consecutive, exhausted := budget.Observe(key, false, 2); consecutive != 1 || exhausted {
		t.Fatalf("first no-progress wait = (%d, %v), want (1, false)", consecutive, exhausted)
	}
	if consecutive, exhausted := budget.Observe(key, false, 2); consecutive != 2 || !exhausted {
		t.Fatalf("second no-progress wait = (%d, %v), want (2, true)", consecutive, exhausted)
	}
	// The gate stays closed until progress or a reset.
	if consecutive, exhausted := budget.Exhausted(key, 2); consecutive != 2 || !exhausted {
		t.Fatalf("exhausted before opening = (%d, %v), want (2, true)", consecutive, exhausted)
	}
}

func TestWaitBudgetProgressResetsTheCounter(t *testing.T) {
	budget := &WaitBudget{}
	key := "parent|turn-1"

	budget.Observe(key, false, 2)
	if consecutive, exhausted := budget.Observe(key, true, 2); consecutive != 0 || exhausted {
		t.Fatalf("progress must reset the counter, got (%d, %v)", consecutive, exhausted)
	}
	if _, exhausted := budget.Exhausted(key, 2); exhausted {
		t.Fatal("a reset budget must not stay exhausted")
	}
}

func TestWaitBudgetResetAndIsolation(t *testing.T) {
	budget := &WaitBudget{}
	budget.Observe("parent|turn-1", false, 1)
	budget.Observe("parent|turn-1", false, 1)

	if _, exhausted := budget.Exhausted("parent|turn-2", 1); exhausted {
		t.Fatal("another turn's budget must not be affected")
	}
	budget.Reset("parent|turn-1")
	if _, exhausted := budget.Exhausted("parent|turn-1", 1); exhausted {
		t.Fatal("Reset must clear the counter")
	}
}

func TestWaitBudgetDisabledAndEmptyKey(t *testing.T) {
	budget := &WaitBudget{}
	if consecutive, exhausted := budget.Observe("parent|turn-1", false, 0); consecutive != 0 || exhausted {
		t.Fatalf("limit=0 must disable the budget, got (%d, %v)", consecutive, exhausted)
	}
	if consecutive, exhausted := budget.Observe("", false, 2); consecutive != 0 || exhausted {
		t.Fatalf("an empty key must not accumulate, got (%d, %v)", consecutive, exhausted)
	}
	if _, exhausted := (*WaitBudget)(nil).Exhausted("parent|turn-1", 2); exhausted {
		t.Fatal("a nil budget must stay disarmed")
	}
}
