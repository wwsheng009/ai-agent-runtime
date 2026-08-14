package commands

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
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
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
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
	select {
	case <-handleDone:
	case <-time.After(10 * time.Second):
		t.Fatal("stream coalescing handler did not finish")
	}
	// Leave the bridge clean: drop any coalesced events (no consumer runs).
	bridge.streamMu.Lock()
	bridge.pendingStreams = nil
	bridge.pendingStreamsBytes = 0
	bridge.streamMu.Unlock()
}

func TestStreamMergeKeyIncludesIdentityAndSequence(t *testing.T) {
	base := runtimeevents.Event{Type: runtimechat.EventAssistantDelta, TraceID: "trace-1"}
	ev1 := base
	ev1.Payload = map[string]interface{}{"turn_id": "turn-a", "stream_id": "stream-a", "sequence": uint64(1)}
	ev2 := base
	ev2.Payload = map[string]interface{}{"turn_id": "turn-a", "stream_id": "stream-a", "sequence": uint64(2)}
	ev3 := base
	ev3.Payload = map[string]interface{}{"turn_id": "turn-b", "stream_id": "stream-a", "sequence": uint64(1)}
	ev4 := base
	ev4.Payload = map[string]interface{}{"turn_id": "turn-a", "stream_id": "stream-b"}

	if streamMergeKey(ev1) == streamMergeKey(ev2) {
		t.Fatal("distinct sequences must not share a coalescing key")
	}
	if streamMergeKey(ev1) == streamMergeKey(ev3) {
		t.Fatal("distinct turn ids must not share a coalescing key")
	}
	if streamMergeKey(ev1) == streamMergeKey(ev4) {
		t.Fatal("distinct stream ids must not share a coalescing key")
	}
	legacy := runtimeevents.Event{Type: runtimechat.EventAssistantDelta, TraceID: "trace-1", Payload: map[string]interface{}{"delta": "a"}}
	if streamMergeKey(legacy) != streamMergeKey(base) {
		t.Fatal("identity-free legacy deltas should remain mergeable by type+trace")
	}
}

func TestEnqueueStreamEventBoundsPendingCount(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "pending-count"}})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill"}, size: 1}
	t.Cleanup(func() {
		bridge.streamMu.Lock()
		bridge.pendingStreams = nil
		bridge.pendingStreamsBytes = 0
		bridge.streamMu.Unlock()
	})

	for i := 0; i < chatStreamCoalescePendingLimit+20; i++ {
		bridge.Handle(runtimeevents.Event{
			Type:    runtimechat.EventAssistantDelta,
			TraceID: fmt.Sprintf("trace-%d", i),
			Payload: map[string]interface{}{"delta": "x"},
		})
	}
	bridge.streamMu.Lock()
	count := len(bridge.pendingStreams)
	bytes := bridge.pendingStreamsBytes
	bridge.streamMu.Unlock()
	if count > chatStreamCoalescePendingLimit {
		t.Fatalf("pending count = %d, want <= %d", count, chatStreamCoalescePendingLimit)
	}
	if bytes > chatStreamCoalescePendingByteLimit {
		t.Fatalf("pending bytes = %d, want <= %d", bytes, chatStreamCoalescePendingByteLimit)
	}
}

func TestEnqueueStreamEventCoalescesContiguousSequenceIntoOnePendingEntry(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "pending-contiguous"}})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill"}, size: 1}
	t.Cleanup(func() {
		bridge.streamMu.Lock()
		bridge.pendingStreams = nil
		bridge.pendingStreamsBytes = 0
		bridge.streamMu.Unlock()
	})

	const total = uint64(chatStreamCoalescePendingLimit + 20)
	var want strings.Builder
	for sequence := uint64(1); sequence <= total; sequence++ {
		chunk := fmt.Sprintf("d%d-", sequence)
		want.WriteString(chunk)
		bridge.Handle(runtimeevents.Event{
			Type:    runtimechat.EventAssistantDelta,
			TraceID: "trace-contiguous",
			Payload: map[string]interface{}{
				"turn_id": "turn-contiguous", "stream_id": "stream-contiguous",
				"sequence": sequence, "delta": chunk,
			},
		})
	}

	bridge.streamMu.Lock()
	count := len(bridge.pendingStreams)
	var text string
	var sequence uint64
	hasSequence := false
	if count > 0 {
		text = streamEventText(bridge.pendingStreams[0].event)
		sequence, hasSequence = assistantEventSequence(bridge.pendingStreams[0].event)
	}
	bytes := bridge.pendingStreamsBytes
	bridge.streamMu.Unlock()

	if count != 1 {
		t.Fatalf("contiguous stream pending count = %d, want 1", count)
	}
	if !hasSequence || sequence != total {
		t.Fatalf("merged sequence = (%d,%v), want %d", sequence, hasSequence, total)
	}
	if from, ok := streamCoalescedFrom(bridge.pendingStreams[0].event); !ok || from != 1 {
		t.Fatalf("merged interval start = (%d,%v), want (1,true)", from, ok)
	}
	if text != want.String() {
		t.Fatalf("merged text length = %d, want %d", len(text), want.Len())
	}
	if bytes <= 0 || bytes > chatStreamCoalescePendingByteLimit {
		t.Fatalf("pending bytes = %d, want in (0,%d]", bytes, chatStreamCoalescePendingByteLimit)
	}

	// A sequence gap must stay a separate pending entry: merging across the
	// gap would hide the missing delta from sequence-ordered assembly.
	bridge.Handle(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		TraceID: "trace-contiguous",
		Payload: map[string]interface{}{
			"turn_id": "turn-contiguous", "stream_id": "stream-contiguous",
			"sequence": total + 2, "delta": "gap-skip",
		},
	})
	bridge.streamMu.Lock()
	count = len(bridge.pendingStreams)
	bridge.streamMu.Unlock()
	if count != 2 {
		t.Fatalf("pending count after sequence gap = %d, want 2", count)
	}
}

func TestOrderAssistantDeltaAdvancesPastCoalescedInterval(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "order-coalesced"}})
	bridge.BeginRun()
	bridge.renderMu.Lock()
	bridge.activeTurnID = "turn-order-coalesced"
	bridge.renderMu.Unlock()
	t.Cleanup(func() {
		bridge.renderMu.Lock()
		bridge.assistantStreams = nil
		bridge.renderMu.Unlock()
	})

	deltaEvent := func(sequence uint64, delta string) runtimeevents.Event {
		return runtimeevents.Event{
			Type: runtimechat.EventAssistantDelta, TraceID: "trace-order-coalesced",
			Payload: map[string]interface{}{
				"turn_id": "turn-order-coalesced", "stream_id": "stream-order-coalesced",
				"sequence": sequence, "delta": delta,
			},
		}
	}
	coalesced := deltaEvent(3, "ABC")
	coalesced.Payload[streamCoalescedFromKey] = uint64(1)

	ordered, handled := bridge.orderAssistantDelta(coalesced)
	if !handled || len(ordered) != 1 {
		t.Fatalf("first coalesced interval ordered=%v handled=%v, want one event", ordered, handled)
	}
	bridge.renderMu.Lock()
	state := bridge.assistantStreams["stream-order-coalesced"]
	next := uint64(0)
	tainted := false
	if state != nil {
		next = state.nextSequence
		tainted = state.tainted
	}
	bridge.renderMu.Unlock()
	if state == nil || next != 4 || tainted {
		t.Fatalf("after interval 1..3: next=%d tainted=%v, want next=4 tainted=false", next, tainted)
	}

	second := deltaEvent(6, "DEF")
	second.Payload[streamCoalescedFromKey] = uint64(4)
	ordered, handled = bridge.orderAssistantDelta(second)
	if !handled || len(ordered) != 1 {
		t.Fatalf("second coalesced interval ordered=%v handled=%v, want one event", ordered, handled)
	}
	bridge.renderMu.Lock()
	state = bridge.assistantStreams["stream-order-coalesced"]
	next = 0
	if state != nil {
		next = state.nextSequence
	}
	bridge.renderMu.Unlock()
	if state == nil || next != 7 {
		t.Fatalf("after interval 4..6: next=%d, want 7", next)
	}
}

func TestEnqueueStreamEventBoundsPendingBytes(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "pending-bytes"}})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill"}, size: 1}
	t.Cleanup(func() {
		bridge.streamMu.Lock()
		bridge.pendingStreams = nil
		bridge.pendingStreamsBytes = 0
		bridge.streamMu.Unlock()
	})

	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta, TraceID: "trace-big-a",
		Payload: map[string]interface{}{"delta": strings.Repeat("a", 700<<10)},
	})
	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventAssistantDelta, TraceID: "trace-big-b",
		Payload: map[string]interface{}{"delta": strings.Repeat("b", 500<<10)},
	})

	bridge.streamMu.Lock()
	bytes := bridge.pendingStreamsBytes
	count := len(bridge.pendingStreams)
	var lastText string
	if count > 0 {
		lastText = streamEventText(bridge.pendingStreams[count-1].event)
	}
	bridge.streamMu.Unlock()
	if bytes > chatStreamCoalescePendingByteLimit {
		t.Fatalf("pending bytes = %d, want <= %d", bytes, chatStreamCoalescePendingByteLimit)
	}
	if strings.Contains(lastText, "b") {
		t.Fatalf("oversized delta was retained; last pending contains new stream text")
	}
}

func TestAssistantTerminalDropsStalePendingAndEnqueues(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "terminal-drop"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.BeginRun()
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill"}, size: 1}
	t.Cleanup(func() {
		bridge.streamMu.Lock()
		bridge.pendingStreams = nil
		bridge.pendingStreamsBytes = 0
		bridge.streamMu.Unlock()
	})

	delta := func(turnID, streamID string) runtimeevents.Event {
		return runtimeevents.Event{
			Type: runtimechat.EventAssistantDelta, TraceID: "trace-" + turnID,
			Payload: map[string]interface{}{"turn_id": turnID, "stream_id": streamID, "delta": "partial " + streamID},
		}
	}
	bridge.Handle(delta("turn-1", "stream-1"))
	bridge.Handle(delta("turn-1", "stream-1"))
	bridge.Handle(delta("turn-1", "stream-2"))

	<-bridge.eventQueue
	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, TraceID: "trace-turn-1",
		Payload: map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "content": "final"},
	})

	bridge.streamMu.Lock()
	count := len(bridge.pendingStreams)
	var remaining string
	if count > 0 {
		remaining = streamEventText(bridge.pendingStreams[0].event)
	}
	bridge.streamMu.Unlock()
	if count != 1 || !strings.Contains(remaining, "stream-2") {
		t.Fatalf("pending after terminal = count %d remaining %q, want only stream-2", count, remaining)
	}
	select {
	case queued := <-bridge.eventQueue:
		if queued.event.Type != runtimechat.EventAssistantMessage {
			t.Fatalf("queued type = %q, want assistant_message", queued.event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was not enqueued")
	}
}

func TestStreamingRuntimeEventPostDropsAfterBoundedWaitWhenMailboxStalled(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "mailbox-full"}}
	coordinator := newChatInteractionCoordinator(session)
	session.Interaction = coordinator
	// Inject a one-slot actor whose Run loop is never started, so the mailbox
	// stays full for the whole bounded retry window.
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, nil, nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	t.Cleanup(func() { actor.Close() })
	if !actor.TryPost(ui.Resize{}) {
		t.Fatal("failed to fill the one-slot mailbox")
	}
	if stats := actor.Stats(); stats.Pending != 1 {
		t.Fatalf("mailbox pending = %d, want 1 for a full mailbox", stats.Pending)
	}

	bridge := newChatRuntimeEventBridge(session)
	bridge.uiActionPostTimeout = 100 * time.Millisecond
	bridge.BeginRun()
	epoch := bridge.runEpoch
	start := time.Now()
	accepted, legacyOK := bridge.postRuntimeEventToUIActorWithEpoch(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		TraceID: "stream-1",
		Payload: map[string]interface{}{"delta": "x"},
	}, epoch)
	elapsed := time.Since(start)
	if accepted || legacyOK {
		t.Fatalf("streaming post = (%v,%v), want (false,false) with full mailbox", accepted, legacyOK)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("streaming post returned after %v; expected bounded retry before drop", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("streaming post waited %v; expected bounded drop", elapsed)
	}
}
