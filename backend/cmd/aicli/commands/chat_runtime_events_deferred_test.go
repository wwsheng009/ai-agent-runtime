package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// newDeferredQueueTestBridge builds a bridge whose bounded queue is already
// full and whose consumer never runs, which is exactly the state a busy
// (rather than dead) UI actor leaves the bridge in.
func newDeferredQueueTestBridge(t *testing.T, sessionID string) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.eventQueueByteLimit = 0
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill", SessionID: sessionID}, size: 1}
	t.Cleanup(func() {
		bridge.deferredMu.Lock()
		bridge.deferredQueue = nil
		bridge.deferredBytes = 0
		bridge.deferredMu.Unlock()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			bridge.deferredMu.Lock()
			running := bridge.deferredWorkerRunning
			bridge.deferredMu.Unlock()
			if !running {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
	return bridge
}

// TestNonStreamEventPublisherDoesNotBlockOnFullQueue is the regression test for
// the reported stall: a full bounded queue used to block the publisher (the
// provider stream callback chain) for up to uiActionPostBudget (5s) per event,
// which stretched child-agent LLM calls to ~112s while the UI actor was behind.
// The publisher must now return within the short admission budget and the event
// must still be delivered once the consumer frees capacity.
func TestNonStreamEventPublisherDoesNotBlockOnFullQueue(t *testing.T) {
	const sessionID = "defer-budget"
	bridge := newDeferredQueueTestBridge(t, sessionID)

	event := runtimeevents.Event{
		Type:      "tool.requested",
		SessionID: sessionID,
		Payload:   map[string]interface{}{"tool_call_id": "call-1", "tool_name": "read_file"},
	}
	start := time.Now()
	bridge.Handle(event)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("non-streaming Handle blocked the publisher for %s; want <= ~%s", elapsed, chatRuntimeNonStreamEnqueueBudget)
	}

	// Free one slot: the deferred worker must deliver the event without it ever
	// having blocked the caller.
	select {
	case <-bridge.eventQueue:
	case <-time.After(2 * time.Second):
		t.Fatal("filler event was never consumed")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case queued := <-bridge.eventQueue:
			if queued.event.Type == event.Type {
				return
			}
		case <-deadline:
			t.Fatalf("deferred %q event was never delivered", event.Type)
		}
	}
}

// TestDeferredRuntimeEventsPreserveOrder verifies FIFO delivery order, which is
// what lets the deferred path replace a blocking enqueue without reordering the
// non-streaming event family.
func TestDeferredRuntimeEventsPreserveOrder(t *testing.T) {
	const sessionID = "defer-order"
	bridge := newDeferredQueueTestBridge(t, sessionID)

	types := []string{"tool.requested", "tool.completed", "assistant.message"}
	for _, typ := range types {
		if !bridge.deferRuntimeEvent(runtimeevents.Event{Type: typ, SessionID: sessionID}, 1) {
			t.Fatalf("deferred %q was rejected before the backlog limit", typ)
		}
	}

	// Draining the queue one slot at a time must yield the deferred events in
	// the order they were deferred.
	<-bridge.eventQueue
	for index, want := range types {
		select {
		case queued := <-bridge.eventQueue:
			if queued.event.Type != want {
				t.Fatalf("deferred event %d = %q, want %q (FIFO order violated)", index, queued.event.Type, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("deferred event %d (%s) was never delivered", index, want)
		}
	}
}

// TestDeferredRuntimeEventBacklogIsBounded verifies that a permanently stalled
// consumer degrades to logged drops instead of unbounded backlog growth.
func TestDeferredRuntimeEventBacklogIsBounded(t *testing.T) {
	const sessionID = "defer-bound"
	// The fresh bridge keeps its consumer slot occupied, so the deferred worker
	// cannot drain while the backlog grows past its cap.
	bridge := newDeferredQueueTestBridge(t, sessionID)

	// Refill the backlog past its cap: further events are dropped (and
	// counted) rather than growing the queue without bound.
	for i := 0; i < chatRuntimeDeferredEventLimit; i++ {
		if !bridge.deferRuntimeEvent(runtimeevents.Event{Type: "tool.progress", SessionID: sessionID}, 1) {
			t.Fatalf("deferred event %d rejected before the backlog limit", i)
		}
	}
	if bridge.deferRuntimeEvent(runtimeevents.Event{Type: "tool.progress", SessionID: sessionID}, 1) {
		t.Fatal("deferred backlog accepted an event past its limit")
	}
	bridge.deferredMu.Lock()
	dropped := bridge.deferredDropped
	backlog := len(bridge.deferredQueue)
	bridge.deferredMu.Unlock()
	if dropped == 0 {
		t.Fatal("dropping past the deferred backlog limit was not counted")
	}
	if backlog > chatRuntimeDeferredEventLimit {
		t.Fatalf("deferred backlog grew to %d, limit %d", backlog, chatRuntimeDeferredEventLimit)
	}
	if pending, _, statsDropped := bridge.deferredQueueStats(); pending != backlog || statsDropped != dropped {
		t.Fatalf("deferredQueueStats = (pending %d, dropped %d), want (%d, %d) — /debug would misreport the overflow queue",
			pending, statsDropped, backlog, dropped)
	}
}

// TestDeferredBacklogKeepsHandleEventsInOrder verifies that a non-streaming
// event published through Handle while the overflow queue is non-empty joins
// that FIFO instead of overtaking it through a direct enqueue.
func TestDeferredBacklogKeepsHandleEventsInOrder(t *testing.T) {
	const sessionID = "defer-handle-order"
	bridge := newDeferredQueueTestBridge(t, sessionID)

	if !bridge.deferRuntimeEvent(runtimeevents.Event{Type: "tool.requested", SessionID: sessionID}, 1) {
		t.Fatal("first deferred event was rejected")
	}
	bridge.Handle(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: sessionID,
		Payload:   map[string]interface{}{"tool_call_id": "call-1", "tool_name": "read_file"},
	})

	<-bridge.eventQueue
	for _, want := range []string{"tool.requested", "tool.completed"} {
		select {
		case queued := <-bridge.eventQueue:
			if queued.event.Type != want {
				t.Fatalf("delivered %q, want %q (deferred FIFO order violated)", queued.event.Type, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q was never delivered", want)
		}
	}
}

// TestWaitDeferredDrainReportsPendingBacklog verifies EndRun's drain barrier
// sees a stalled deferred backlog instead of reporting a clean drain.
func TestWaitDeferredDrainReportsPendingBacklog(t *testing.T) {
	const sessionID = "defer-drain"
	bridge := newDeferredQueueTestBridge(t, sessionID)
	if !bridge.deferRuntimeEvent(runtimeevents.Event{Type: "tool.completed", SessionID: sessionID}, 1) {
		t.Fatal("deferred event was rejected")
	}
	if bridge.waitDeferredDrain(60 * time.Millisecond) {
		t.Fatal("drain reported success while the consumer was still stalled")
	}
	<-bridge.eventQueue
	if !bridge.waitDeferredDrain(5 * time.Second) {
		t.Fatal("drain did not complete after the consumer caught up")
	}
}

// TestWaitForCurrentEventsSeesDeferredBacklog is the regression test for the
// settle predicate: deferred events are accepted by the bridge but live outside
// the bounded queue, so enqueued/processed alone reported a clean drain while a
// backlog was still pending. Callers (actor-executor prompt/goal result, EndRun
// barrier) would then read a half-delivered timeline.
func TestWaitForCurrentEventsSeesDeferredBacklog(t *testing.T) {
	const sessionID = "defer-settle"
	settled := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	if !settled.WaitForCurrentEvents(time.Second) {
		t.Fatal("idle bridge with an empty backlog must still report a settled drain")
	}

	bridge := newDeferredQueueTestBridge(t, sessionID)
	if !bridge.deferRuntimeEvent(runtimeevents.Event{Type: "tool.completed", SessionID: sessionID}, 1) {
		t.Fatal("deferred event was rejected")
	}
	if bridge.WaitForCurrentEvents(200 * time.Millisecond) {
		t.Fatal("drain reported settled while the deferred backlog was still pending")
	}
}

// TestDeferredDropLoggingIsThrottled pins the publisher-side log throttle: a
// full backlog drops every subsequent event, and one payload marshal + debug
// write per drop would rebuild the I/O amplification the deferred queue exists
// to avoid.
func TestDeferredDropLoggingIsThrottled(t *testing.T) {
	if !shouldLogDeferredDrop(1) {
		t.Fatal("first drop must be logged so the degraded state is not silent")
	}
	if shouldLogDeferredDrop(2) {
		t.Fatal("second drop must not be logged")
	}
	if !shouldLogDeferredDrop(chatRuntimeDeferredDropLogInterval) {
		t.Fatalf("drop %d must be logged", chatRuntimeDeferredDropLogInterval)
	}
	if shouldLogDeferredDrop(chatRuntimeDeferredDropLogInterval + 1) {
		t.Fatal("drop between intervals must not be logged")
	}
	if !shouldLogDeferredDrop(2 * chatRuntimeDeferredDropLogInterval) {
		t.Fatalf("drop %d must be logged", 2*chatRuntimeDeferredDropLogInterval)
	}
}
