package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

func TestSubagentBatchCoordinatorLifecycleProjectionIsBestEffort(t *testing.T) {
	store := testStore(t)
	exec := &fakeExecutor{
		results: []SubagentResult{{ID: "t1", Success: true, Summary: "done"}},
	}
	projected := make(chan BatchTerminalLifecycle, 1)
	delivered := make(chan struct{})
	var deliveredOnce sync.Once
	coordinator := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
		sink: func(_ context.Context, _ BatchTerminalNotification) BatchTerminalDelivery {
			deliveredOnce.Do(func() { close(delivered) })
			return BatchTerminalDelivery{Status: BatchTerminalDeliveryPersisted}
		},
		lifecycleProjector: func(_ context.Context, event BatchTerminalLifecycle) error {
			projected <- event
			return errors.New("supervision temporarily unavailable")
		},
	}

	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "parent-1",
		RootScopeID:     "parent-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Goal: "complete"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCompleted {
		t.Fatalf("terminal status = %s, want completed", term.Status)
	}
	select {
	case event := <-projected:
		if event.BatchID != batch.BatchID || event.Status != subagentbatch.BatchCompleted {
			t.Fatalf("projected event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle projector was not called")
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("projection failure prevented terminal delivery")
	}
}
