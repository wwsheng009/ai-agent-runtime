package runtimeapi

import (
	"context"
	"testing"
	"time"
)

// TestScheduleReminderRunContextDetachesParentCancellation locks the scheduler
// contract: Runtime.drive cancels the deliver context immediately after
// Deliver returns, but the actor turn must continue. A regression here makes a
// session reminder look "delivered" while its model run is canceled at startup.
func TestScheduleReminderRunContextDetachesParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	runCtx, cancelRun := scheduleReminderRunContext(parent)
	defer cancelRun()

	cancelParent()

	select {
	case <-runCtx.Done():
		t.Fatalf("run context inherited parent cancellation: %v", runCtx.Err())
	case <-time.After(20 * time.Millisecond):
	}

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("run context must have a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > scheduleSessionReminderRunTimeout {
		t.Fatalf("run context deadline out of range: %s", remaining)
	}
}
