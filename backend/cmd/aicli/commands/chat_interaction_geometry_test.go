package commands

import (
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestPrimaryTerminalGeometryDoesNotWaitForCoordinatorLock(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coordinator.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(91, 33)
	coordinator.uiSurface.Store(surface)

	// The presenter calls this probe from UIController effect delivery, so it
	// must remain independent of the producer-side coordinator lock.
	coordinator.mu.Lock()
	done := make(chan struct{})
	var width, height int
	var ok bool
	go func() {
		width, height, ok = coordinator.primaryTerminalGeometry()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		coordinator.mu.Unlock()
		t.Fatal("primaryTerminalGeometry blocked on coordinator.mu")
	}
	coordinator.mu.Unlock()

	if !ok || width != 91 || height != 33 {
		t.Fatalf("geometry = (%d, %d, %v), want (91, 33, true)", width, height, ok)
	}
	coordinator.Shutdown()
	if _, _, ok := coordinator.primaryTerminalGeometry(); ok {
		t.Fatal("primaryTerminalGeometry remained available after shutdown")
	}
}

func TestSurfaceFacadePostDoesNotDeadlockBehindCoordinatorLock(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coordinator.Shutdown)
	blockReducer := make(chan struct{})
	var releaseOnce sync.Once
	started := make(chan struct{})
	var startedOnce bool
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(_ uint64, action ui.UIAction) []ui.Effect {
		if event, ok := action.(ui.RuntimeEvent); ok && event.Kind == "blocking" {
			if !startedOnce {
				startedOnce = true
				close(started)
			}
			<-blockReducer
			// Simulate the coordinator work performed by the real reducer after
			// the blocking event has been released.
			coordinator.mu.Lock()
			coordinator.mu.Unlock()
		}
		return nil
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	defer func() {
		releaseOnce.Do(func() { close(blockReducer) })
		actor.Close()
		actor.WaitIdle()
	}()

	if !coordinator.postUIAction(ui.RuntimeEvent{Kind: "blocking"}) {
		t.Fatal("failed to start blocking reducer")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking reducer did not start")
	}
	if !coordinator.postUIAction(ui.RuntimeEvent{Kind: "queued"}) {
		t.Fatal("failed to fill actor mailbox")
	}

	posted := make(chan bool, 1)
	producerLocked := make(chan struct{})
	go func() {
		coordinator.mu.Lock()
		close(producerLocked)
		ok := coordinator.postSurfaceFacadeAction(ui.SetStatusModelsAction{})
		coordinator.mu.Unlock()
		posted <- ok
	}()
	select {
	case <-producerLocked:
	case <-time.After(time.Second):
		t.Fatal("producer did not acquire coordinator lock")
	}
	releaseOnce.Do(func() { close(blockReducer) })
	select {
	case ok := <-posted:
		if !ok {
			t.Fatal("deferred facade action was rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("facade post blocked while holding coordinator lock")
	}
	actor.WaitIdle()
}
