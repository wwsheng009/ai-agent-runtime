package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEvents_StreamCoalescingConcurrentWithBeginRun is a lock-order
// regression test: BeginRun clears pending streams and the coalescing path
// takes streamMu then renderMu, so BeginRun must never hold renderMu while
// taking streamMu (that inversion deadlocks under a full queue). Run with
// -race to also catch data races.
func TestChatRuntimeEvents_StreamCoalescingConcurrentWithBeginRun(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)

	stop := make(chan struct{})
	beginDone := make(chan struct{})
	go func() {
		defer close(beginDone)
		for i := 0; i < 300; i++ {
			bridge.BeginRun()
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	go func() {
		for i := 0; i < 3000; i++ {
			bridge.Handle(runtimeevents.Event{
				Type:      runtimechat.EventAssistantDelta,
				SessionID: "lead-session",
				Payload:   map[string]interface{}{"delta": "x"},
			})
		}
	}()

	select {
	case <-beginDone:
	case <-time.After(10 * time.Second):
		close(stop)
		t.Fatal("deadlock: BeginRun and stream coalescing contend on locks")
	}
	// Leave the bridge clean: drop any coalesced events (no consumer runs).
	bridge.streamMu.Lock()
	bridge.pendingStreams = nil
	bridge.streamMu.Unlock()
}
