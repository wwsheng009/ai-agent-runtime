package commands

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// newClassifyTestBridge builds a bridge whose bounded queue is already full (so
// non-streaming events take the deferred path) with the classification mode
// pinned: newChatRuntimeEventBridge reads AICLI_EVENT_BRIDGE_CLASSIFY from the
// ambient environment and the tests must not depend on it.
func newClassifyTestBridge(t *testing.T, sessionID string, mode chatEventClassifyMode) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newDeferredQueueTestBridge(t, sessionID)
	bridge.classifyMode = mode
	return bridge
}

// deferredQueueSlotTypes snapshots the overflow queue's event types in FIFO
// order.
func deferredQueueSlotTypes(bridge *chatRuntimeEventBridge) []string {
	bridge.deferredMu.Lock()
	defer bridge.deferredMu.Unlock()
	types := make([]string, 0, len(bridge.deferredQueue))
	for _, slot := range bridge.deferredQueue {
		if slot == nil {
			continue
		}
		types = append(types, slot.event.Type)
	}
	return types
}

// waitForBridgeCondition polls a bridge predicate so tests do not depend on the
// retry-channel pacing (5ms per attempt).
func waitForBridgeCondition(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestClassifyChatRuntimeEventMapsDeliveryClasses pins the §6.1.1 mapping. The
// classes decide the delivery path, so a silent reclassification would change
// loss semantics.
func TestClassifyChatRuntimeEventMapsDeliveryClasses(t *testing.T) {
	critical := []string{
		runtimechat.EventAssistantMessage,
		runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		"run_end",
		"run.end",
		runtimechat.EventToolFinished,
		"tool.completed",
		"tool_failed",
		runtimechat.EventApprovalRequested,
		runtimechat.EventApprovalResolved,
		runtimechat.EventQuestionAsked,
		runtimechat.EventQuestionAnswered,
		runtimechat.EventSessionCompactFailed,
		"subagent.completed",
		"subagent.batch.failed",
	}
	for _, eventType := range critical {
		require.Equal(t, eventClassCritical, classifyChatRuntimeEvent(eventType), eventType)
	}

	coalescible := []string{
		runtimeobserve.EventUsageUpdated,
		"aicli.chat.dynamic_status",
		"dynamic_status",
		"tool.progress",
		"cache_request_finished",
		runtimechat.EventContextReconciled,
	}
	for _, eventType := range coalescible {
		require.Equal(t, eventClassCoalescible, classifyChatRuntimeEvent(eventType), eventType)
	}

	ordered := []string{
		runtimechat.EventToolStarted,
		"compact.started",
		"compact.completed",
		"checkpoint_created",
		"mailbox_received",
		"subagent.started",
	}
	for _, eventType := range ordered {
		require.Equal(t, eventClassOrdered, classifyChatRuntimeEvent(eventType), eventType)
	}

	for _, eventType := range []string{
		runtimechat.EventAssistantDelta, runtimechat.EventAssistantReasoning,
		// 本地 ReAct loop 在总线上发的是点分隔别名，必须与下划线常量同类
		// （否则降级 ordered，在高频流下被 deferred 队列整批丢弃）。
		"assistant.delta", "assistant.reasoning",
	} {
		require.Equal(t, eventClassStream, classifyChatRuntimeEvent(eventType), eventType)
	}
	// 大小写与空白容错：总线上出现过大小写混用的类型。
	require.Equal(t, eventClassCritical, classifyChatRuntimeEvent(" SESSION_END "))
}

// TestChatEventBridgeClassifyModeFromEnv pins the rollback switch: an explicit
// off/observe must win, and an unknown value must not silently disable the
// hardening (§6.1.6).
func TestChatEventBridgeClassifyModeFromEnv(t *testing.T) {
	cases := []struct {
		value string
		want  chatEventClassifyMode
	}{
		{"", chatEventClassifyEnforce},
		{"off", chatEventClassifyOff},
		{" OFF ", chatEventClassifyOff},
		{"false", chatEventClassifyOff},
		{"observe", chatEventClassifyObserve},
		{"Observe", chatEventClassifyObserve},
		{"enforce", chatEventClassifyEnforce},
		{"typo-mode", chatEventClassifyEnforce},
	}
	for _, tc := range cases {
		t.Setenv("AICLI_EVENT_BRIDGE_CLASSIFY", tc.value)
		require.Equal(t, tc.want, chatEventBridgeClassifyModeFromEnv(), tc.value)
	}
}

// TestChatRuntimeEvents_CoalescesUsageAndStatusInDeferredQueue covers §6.1.7
// item 1: repeated latest-wins events must occupy one slot carrying the newest
// value instead of growing the backlog (that growth is what pushed the 512-slot
// FIFO over its cap in the field).
func TestChatRuntimeEvents_CoalescesUsageAndStatusInDeferredQueue(t *testing.T) {
	const sessionID = "classify-merge"
	bridge := newClassifyTestBridge(t, sessionID, chatEventClassifyEnforce)

	for i := 0; i < 3; i++ {
		require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{
			Type:      runtimeobserve.EventUsageUpdated,
			SessionID: sessionID,
			Payload:   map[string]interface{}{"turn_id": "turn-1", "seq": i},
		}, 1), "usage event %d", i)
	}
	for i := 0; i < 2; i++ {
		require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{
			Type:      chatWebDynamicStatusBusEvent,
			SessionID: sessionID,
			Payload:   map[string]interface{}{"seq": i},
		}, 1), "dynamic status %d", i)
	}

	bridge.deferredMu.Lock()
	slots := append([]*chatRuntimeQueuedEvent(nil), bridge.deferredQueue...)
	merged := bridge.deferredMerged
	indexSize := len(bridge.deferredIndex)
	newest := make([]runtimeevents.Event, 0, len(slots))
	for _, slot := range slots {
		// 在途槽位把"投递中"窗口里到达的最新值挂在 pending 上（§6.1.3），
		// 断言必须同时覆盖两者，避免依赖 worker 的调度时机。
		if slot.hasPending {
			newest = append(newest, slot.pendingEvent)
			continue
		}
		newest = append(newest, slot.event)
	}
	bridge.deferredMu.Unlock()

	require.Len(t, slots, 2, "usage + dynamic status must each hold exactly one slot")
	require.Equal(t, uint64(3), merged, "3 of the 5 events must have been merged in place")
	require.Equal(t, 2, indexSize, "the merge index must hold both live slots")
	require.Equal(t, runtimeobserve.EventUsageUpdated, newest[0].Type)
	require.Equal(t, 2, newest[0].Payload["seq"], "the slot must carry the newest value")
	require.Equal(t, chatWebDynamicStatusBusEvent, newest[1].Type)
	require.Equal(t, 1, newest[1].Payload["seq"])

	stats := bridge.deferredQueueClassStats()
	require.Equal(t, "enforce", stats.Mode)
	require.Equal(t, uint64(3), stats.Merged)
	require.LessOrEqual(t, stats.PeakPending, 2)
	_, _, dropped := bridge.deferredQueueStats()
	require.Zero(t, dropped)
}

// TestChatRuntimeEvents_InFlightSlotPromotesPendingValue pins the window the
// race fix (§8.1) introduced: while the deferred worker delivers the head, the
// slot stays indexed, so a newer same-key value is parked as pending instead of
// being written into the slot the worker reads outside the lock. The newest
// value must still be delivered, in order, without opening a second slot.
func TestChatRuntimeEvents_InFlightSlotPromotesPendingValue(t *testing.T) {
	const sessionID = "classify-in-flight"
	bridge := newClassifyTestBridge(t, sessionID, chatEventClassifyEnforce)

	require.True(t, bridge.deferRuntimeEvent(usageUpdatedEvent(sessionID, 0), 1))
	waitForBridgeCondition(t, "the head slot to enter flight", 2*time.Second, func() bool {
		bridge.deferredMu.Lock()
		defer bridge.deferredMu.Unlock()
		return len(bridge.deferredQueue) == 1 && bridge.deferredQueue[0].inFlight
	})

	require.True(t, bridge.deferRuntimeEvent(usageUpdatedEvent(sessionID, 1), 1),
		"the newer value must be accepted while the head is in flight")
	bridge.deferredMu.Lock()
	require.True(t, bridge.deferredQueue[0].hasPending,
		"the newer value must be parked, not written into the in-flight slot")
	require.Equal(t, 1, bridge.deferredQueue[0].pendingEvent.Payload["seq"])
	require.Len(t, bridge.deferredQueue, 1,
		"latest-wins must not open a second slot while the head is in flight")
	bridge.deferredMu.Unlock()

	// Free the consumer slot: the in-flight value goes out first, then the worker
	// promotes the pending value in place and delivers it.
	<-bridge.eventQueue
	for want := 0; want < 2; want++ {
		select {
		case queued := <-bridge.eventQueue:
			require.Equal(t, runtimeobserve.EventUsageUpdated, queued.event.Type)
			require.Equal(t, want, queued.event.Payload["seq"], "delivery %d", want)
		case <-time.After(5 * time.Second):
			t.Fatalf("delivery %d never reached the bounded queue", want)
		}
	}
	waitForBridgeCondition(t, "the overflow queue to drain", 2*time.Second, func() bool {
		return len(deferredQueueSlotTypes(bridge)) == 0
	})

	stats := bridge.deferredQueueClassStats()
	require.Equal(t, uint64(1), stats.Merged, "the newest value must count as a merge")
	require.Equal(t, 1, stats.PeakPending, "the pending value must reuse the in-flight slot")
}

// TestChatRuntimeEvents_EvictsCoalescibleBeforeDroppingOrdered covers §6.1.7
// item 2 and §6.1.4: with the backlog full and only a coalescible slot left to
// shed, a new ordered event must displace that slot rather than be dropped.
func TestChatRuntimeEvents_EvictsCoalescibleBeforeDroppingOrdered(t *testing.T) {
	const sessionID = "classify-evict"
	bridge := newClassifyTestBridge(t, sessionID, chatEventClassifyEnforce)

	// Head: the only coalescible slot, which is what the queue may shed.
	require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{
		Type:      runtimeobserve.EventUsageUpdated,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	}, 1))
	for i := 1; i < chatRuntimeDeferredEventLimit; i++ {
		require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{Type: "checkpoint_created", SessionID: sessionID}, 1), "ordered fill %d", i)
	}
	require.Len(t, deferredQueueSlotTypes(bridge), chatRuntimeDeferredEventLimit)

	require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{Type: "checkpoint_created", SessionID: sessionID}, 1),
		"the ordered event must be admitted by evicting the coalescible slot")

	stats := bridge.deferredQueueClassStats()
	require.Equal(t, uint64(1), stats.Evicted)
	require.Equal(t, uint64(1), stats.EvictedByType[runtimeobserve.EventUsageUpdated])
	_, _, dropped := bridge.deferredQueueStats()
	require.Zero(t, dropped, "nothing may be dropped while a coalescible slot can make room")

	types := deferredQueueSlotTypes(bridge)
	require.Len(t, types, chatRuntimeDeferredEventLimit)
	for _, typ := range types {
		require.NotEqual(t, runtimeobserve.EventUsageUpdated, typ, "the evicted slot must be gone")
	}
}

// drainRuntimeEvents consumes the bounded queue until done() reports the
// expectation is met, feeding every event to observe. A stalled producer fails
// the test instead of hanging it.
func drainRuntimeEvents(t *testing.T, bridge *chatRuntimeEventBridge, timeout time.Duration, done func() bool, observe func(chatRuntimeQueuedEvent)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out draining the runtime event queue")
		}
		select {
		case queued := <-bridge.eventQueue:
			observe(queued)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestChatRuntimeEvents_DeferredStatsExposeClassCounters covers §6.1.7 item 4:
// the /debug surface must expose merged/evicted/dropped-by-class and the
// critical in-flight/shutdown counters, otherwise the degradation is invisible.
func TestChatRuntimeEvents_DeferredStatsExposeClassCounters(t *testing.T) {
	const sessionID = "classify-stats"

	mergeBridge := newClassifyTestBridge(t, sessionID+"-merge", chatEventClassifyEnforce)
	for i := 0; i < 3; i++ {
		require.True(t, mergeBridge.deferRuntimeEvent(runtimeevents.Event{
			Type:      runtimeobserve.EventUsageUpdated,
			SessionID: sessionID + "-merge",
			Payload:   map[string]interface{}{"turn_id": "turn-1"},
		}, 1))
	}
	mergeStats := mergeBridge.deferredQueueClassStats()
	require.Equal(t, "enforce", mergeStats.Mode)
	require.Equal(t, uint64(2), mergeStats.Merged)
	require.Equal(t, 1, mergeStats.PeakPending)
	require.Empty(t, mergeStats.DroppedByClass)

	// Full backlog of ordered events only: nothing coalescible is left to
	// evict, so the overflow must be dropped and attributed by class + type.
	dropBridge := newClassifyTestBridge(t, sessionID+"-drop", chatEventClassifyEnforce)
	ordered := func() runtimeevents.Event {
		return runtimeevents.Event{Type: "checkpoint_created", SessionID: sessionID + "-drop"}
	}
	for i := 0; i < chatRuntimeDeferredEventLimit; i++ {
		require.True(t, dropBridge.deferRuntimeEvent(ordered(), 1), "ordered fill %d", i)
	}
	require.False(t, dropBridge.deferRuntimeEvent(ordered(), 1))

	// A critical event never enters the queue; while the consumer is stalled it
	// waits in the retry channel and must be accounted for at shutdown.
	dropBridge.Handle(runtimeevents.Event{
		Type:      runtimechat.EventToolFinished,
		SessionID: sessionID + "-drop",
		Payload:   map[string]interface{}{"tool_call_id": "call-1", "tool_name": "read_file"},
	})

	stats := dropBridge.deferredQueueClassStats()
	require.Equal(t, "enforce", stats.Mode)
	require.Equal(t, uint64(1), stats.DroppedByClass["ordered"])
	require.Equal(t, uint64(1), stats.DroppedByType["checkpoint_created"])
	require.Zero(t, stats.DroppedByClass["critical"], "critical events never take the drop path")
	require.Equal(t, chatRuntimeDeferredEventLimit, stats.PeakPending)
	require.EqualValues(t, 1, stats.CriticalPeakPending)

	require.True(t, dropBridge.recordCriticalShutdownIfPending())
	stats = dropBridge.deferredQueueClassStats()
	require.EqualValues(t, 1, stats.CriticalAtShutdown)
	require.True(t, stats.Degraded, "a shutdown with critical events in flight must mark the bridge degraded")

	for _, typ := range deferredQueueSlotTypes(dropBridge) {
		require.NotEqual(t, runtimechat.EventToolFinished, typ, "a critical event must not sit in the overflow queue")
	}
}

// TestChatRuntimeEvents_CriticalEventsNeverDropUnderOverflow covers §6.1.7 item
// 3 and §8.4: under a sustained overflow storm the critical set (tool
// boundaries, session end, subagent terminals) must be delivered in full while
// the ordinary firehose degrades.
func TestChatRuntimeEvents_CriticalEventsNeverDropUnderOverflow(t *testing.T) {
	const sessionID = "classify-storm"
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	bridge.classifyMode = chatEventClassifyEnforce
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 8)
	bridge.eventQueueByteLimit = 0
	for i := 0; i < cap(bridge.eventQueue); i++ {
		bridge.eventQueue <- chatRuntimeQueuedEvent{
			event: runtimeevents.Event{Type: "filler", SessionID: sessionID},
			size:  1,
		}
	}
	t.Cleanup(func() {
		bridge.deferredMu.Lock()
		bridge.deferredQueue = nil
		bridge.deferredBytes = 0
		bridge.deferredMu.Unlock()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-bridge.eventQueue:
				continue
			default:
			}
			bridge.deferredMu.Lock()
			running := bridge.deferredWorkerRunning
			bridge.deferredMu.Unlock()
			if !running {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})

	criticalTypes := []string{
		runtimechat.EventToolFinished,
		runtimechat.EventSessionEnd,
		"subagent.batch.failed",
	}
	window := 5 * time.Second
	if testing.Short() {
		window = 500 * time.Millisecond
	}

	injectedCritical := 0
	stormDone := make(chan struct{})
	go func() {
		defer close(stormDone)
		deadline := time.Now().Add(window)
		for i := 0; time.Now().Before(deadline); i++ {
			if i%20 == 0 {
				bridge.Handle(runtimeevents.Event{
					Type:      criticalTypes[(i/20)%len(criticalTypes)],
					SessionID: sessionID,
					Payload:   map[string]interface{}{"marker": i},
				})
				injectedCritical++
			}
			// Ordinary firehose: the coalescible/ordered families that overflowed
			// the 512-slot FIFO in the field.
			bridge.deferRuntimeEvent(runtimeevents.Event{
				Type:      "tool.progress",
				SessionID: sessionID,
				Payload:   map[string]interface{}{"tool_call_id": fmt.Sprintf("call-%d", i)},
			}, 1)
			bridge.deferRuntimeEvent(runtimeevents.Event{Type: "checkpoint_created", SessionID: sessionID}, 1)
			time.Sleep(time.Millisecond)
		}
	}()
	<-stormDone

	require.Positive(t, injectedCritical)
	stats := bridge.deferredQueueClassStats()
	require.Zero(t, stats.DroppedByClass["critical"], "critical events must never be attributed to the drop path")
	require.EqualValues(t, injectedCritical, stats.CriticalPeakPending)
	for _, typ := range deferredQueueSlotTypes(bridge) {
		require.NotEqual(t, eventClassCritical, classifyChatRuntimeEvent(typ), "critical %q must not sit in the overflow queue", typ)
	}

	// Release the consumer: every injected critical event must arrive.
	observedCritical := 0
	drainRuntimeEvents(t, bridge, 30*time.Second, func() bool { return observedCritical >= injectedCritical }, func(queued chatRuntimeQueuedEvent) {
		for _, typ := range criticalTypes {
			if queued.event.Type == typ {
				observedCritical++
				return
			}
		}
	})
	waitForBridgeCondition(t, "critical retry channel to drain", 10*time.Second, func() bool {
		return bridge.deferredQueueClassStats().CriticalPending == 0
	})
	require.Zero(t, bridge.deferredQueueClassStats().DroppedByClass["critical"])
}

// TestChatRuntimeEvents_AssistantTerminalNotDroppedAfterDeltaPurge covers
// §6.1.7 item 5 and §4.1: the terminal assistant_message supersedes its
// coalesced deltas, so the terminal itself must survive a full overflow queue —
// otherwise the whole turn's text disappears (the field-reported content loss).
func TestChatRuntimeEvents_AssistantTerminalNotDroppedAfterDeltaPurge(t *testing.T) {
	const sessionID = "classify-terminal"
	bridge := newClassifyTestBridge(t, sessionID, chatEventClassifyEnforce)

	bridge.enqueueStreamEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "sequence": 1, "text": "partial text"},
	}, 1)
	require.Len(t, bridge.pendingStreams, 1, "the stalled consumer must hold the delta as coalesced backlog")

	bridge.Handle(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": "turn-1", "stream_id": "stream-1", "sequence": 2, "text": "full final text"},
	})
	require.Empty(t, bridge.pendingStreams, "the terminal snapshot supersedes the coalesced deltas")

	// The terminal is waiting in the retry channel; freeing the consumer must
	// deliver it (payload intact) instead of losing the turn's text.
	<-bridge.eventQueue
	delivered := 0
	drainRuntimeEvents(t, bridge, 10*time.Second, func() bool { return delivered > 0 }, func(queued chatRuntimeQueuedEvent) {
		if queued.event.Type != runtimechat.EventAssistantMessage {
			return
		}
		delivered++
		require.Equal(t, "full final text", queued.event.Payload["text"])
	})

	stats := bridge.deferredQueueClassStats()
	require.Zero(t, stats.DroppedByClass["critical"])
	require.Zero(t, stats.DroppedByClass["coalescible"])
}

// usageUpdatedEvent builds a latest-wins usage event for a session.
func usageUpdatedEvent(sessionID string, seq int) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      runtimeobserve.EventUsageUpdated,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": "turn-1", "seq": seq},
	}
}

// TestChatRuntimeEvents_CriticalRetryDoesNotEnterDeferredQueue covers §6.1.7
// item 6 and §6.1.5: a critical event must bypass the overflow FIFO entirely
// (retry channel instead), the ordered family must keep its relative order
// while the critical retry overtakes it, and the in-flight counter must fall
// back to zero once the consumer catches up.
func TestChatRuntimeEvents_CriticalRetryDoesNotEnterDeferredQueue(t *testing.T) {
	const sessionID = "classify-retry"
	bridge := newClassifyTestBridge(t, sessionID, chatEventClassifyEnforce)

	for i := 0; i < chatRuntimeDeferredEventLimit; i++ {
		require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{
			Type:      "checkpoint_created",
			SessionID: sessionID,
			Payload:   map[string]interface{}{"seq": i},
		}, 1), "ordered fill %d", i)
	}

	// Defensive path (deferRuntimeEvent must reroute) and the ordinary Handle
	// path must both keep the critical event out of the FIFO.
	require.True(t, bridge.deferRuntimeEvent(runtimeevents.Event{
		Type:      runtimechat.EventToolFinished,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"tool_call_id": "call-1", "tool_name": "read_file"},
	}, 1))
	bridge.Handle(runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: sessionID})

	require.Len(t, deferredQueueSlotTypes(bridge), chatRuntimeDeferredEventLimit,
		"critical events must not grow the deferred queue")
	for _, typ := range deferredQueueSlotTypes(bridge) {
		require.NotEqual(t, eventClassCritical, classifyChatRuntimeEvent(typ), "critical %q leaked into the overflow queue", typ)
	}
	require.EqualValues(t, 2, bridge.deferredQueueClassStats().CriticalPending,
		"both critical events must be waiting in the retry channel while the consumer is stalled")

	orderedSeqs := make([]int, 0, chatRuntimeDeferredEventLimit)
	criticalSeen := 0
	drainRuntimeEvents(t, bridge, 30*time.Second, func() bool {
		return criticalSeen >= 2 && len(orderedSeqs) >= chatRuntimeDeferredEventLimit
	}, func(queued chatRuntimeQueuedEvent) {
		switch queued.event.Type {
		case runtimechat.EventToolFinished, runtimechat.EventSessionEnd:
			criticalSeen++
		case "checkpoint_created":
			if seq, ok := queued.event.Payload["seq"].(int); ok {
				orderedSeqs = append(orderedSeqs, seq)
			}
		}
	})

	for index, seq := range orderedSeqs {
		require.Equal(t, index, seq, "ordered events must keep FIFO order (index %d)", index)
	}
	waitForBridgeCondition(t, "critical retry channel to drain", 10*time.Second, func() bool {
		return bridge.deferredQueueClassStats().CriticalPending == 0
	})
}

// TestChatRuntimeEvents_ObserveAndOffModesKeepLegacyDelivery is the PR-0
// acceptance check (§7): `off` must restore the pre-hardening behaviour and
// `observe` must only count — no merging, no eviction, no widened critical set.
func TestChatRuntimeEvents_ObserveAndOffModesKeepLegacyDelivery(t *testing.T) {
	const sessionID = "classify-observe"

	observeBridge := newClassifyTestBridge(t, sessionID+"-observe", chatEventClassifyObserve)
	for i := 0; i < 3; i++ {
		require.True(t, observeBridge.deferRuntimeEvent(usageUpdatedEvent(sessionID+"-observe", i), 1))
	}
	require.Len(t, deferredQueueSlotTypes(observeBridge), 3, "observe mode must not merge in place")
	for i := 3; i < chatRuntimeDeferredEventLimit; i++ {
		require.True(t, observeBridge.deferRuntimeEvent(usageUpdatedEvent(sessionID+"-observe", i), 1))
	}
	require.False(t, observeBridge.deferRuntimeEvent(usageUpdatedEvent(sessionID+"-observe", 999), 1))

	observeStats := observeBridge.deferredQueueClassStats()
	require.Equal(t, "observe", observeStats.Mode)
	require.Zero(t, observeStats.Merged)
	require.Zero(t, observeStats.Evicted, "observe mode must not evict")
	require.Equal(t, uint64(1), observeStats.DroppedByClass["coalescible"],
		"observe mode must still attribute drops by class")
	require.Equal(t, uint64(1), observeStats.DroppedByType[runtimeobserve.EventUsageUpdated])
	require.False(t, observeBridge.eventIsCritical(runtimechat.EventAssistantMessage),
		"observe mode must not widen the critical set")
	require.True(t, observeBridge.eventIsCritical("subagent.batch.failed"))

	offBridge := newClassifyTestBridge(t, sessionID+"-off", chatEventClassifyOff)
	for i := 0; i < chatRuntimeDeferredEventLimit; i++ {
		require.True(t, offBridge.deferRuntimeEvent(usageUpdatedEvent(sessionID+"-off", i), 1))
	}
	require.False(t, offBridge.deferRuntimeEvent(usageUpdatedEvent(sessionID+"-off", 999), 1))
	offStats := offBridge.deferredQueueClassStats()
	require.Equal(t, "off", offStats.Mode)
	require.Zero(t, offStats.Merged)
	require.Zero(t, offStats.Evicted)
	require.Empty(t, offStats.DroppedByClass, "off mode must not classify drops")
	require.False(t, offBridge.eventIsCritical(runtimechat.EventAssistantMessage))
	require.False(t, offBridge.eventIsCritical(runtimechat.EventToolFinished))
}
