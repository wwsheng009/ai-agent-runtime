package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// turnBudgetWarningEvent builds the observation event the agent loop publishes at
// the 80% watermark (internal/agent loop.go: EventTurnBudgetWarning +
// turn_budget_level/line/ratio). The turn id matches the active run so the
// bridge's ownership guard treats it as this turn's primary event.
func turnBudgetWarningEvent(sessionID, turnID, level, line string, extra map[string]interface{}) runtimeevents.Event {
	payload := map[string]interface{}{
		"turn_id":           turnID,
		"turn_budget_level": level,
		"turn_budget_line":  line,
		"turn_budget_ratio": 0.8,
	}
	for key, value := range extra {
		payload[key] = value
	}
	return runtimeevents.Event{
		Type:      chatRuntimeTurnBudgetWarningEvent,
		SessionID: sessionID,
		Payload:   payload,
	}
}

// newTurnBudgetTestBridge builds a bridge that owns an active run for turnID,
// which is the state the live TUI is in when the 80% watermark warning arrives.
func newTurnBudgetTestBridge(t *testing.T, sessionID, turnID string) *chatRuntimeEventBridge {
	t.Helper()
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}})
	bridge.setPrimarySessionID(sessionID)
	bridge.renderMu.Lock()
	bridge.runActive = true
	bridge.runStarted = true
	bridge.activeTurnID = turnID
	bridge.retiredTurnIDs = map[string]struct{}{}
	// A non-zero epoch is what the live bridge always has after BeginRun; the
	// queued-event gate (isRunEpochCurrent) rejects epoch 0 as stale.
	bridge.runEpoch = 1
	bridge.renderMu.Unlock()
	return bridge
}

// deliverTurnBudgetEvent feeds one event through the consumer path
// (handleQueuedEvent) exactly as the bridge's worker does. The unit tests do not
// start the worker goroutine, so they must not rely on Handle's enqueue.
func deliverTurnBudgetEvent(bridge *chatRuntimeEventBridge, event runtimeevents.Event) {
	bridge.handleQueuedEvent(chatRuntimeQueuedEvent{event: event, size: 1, epoch: bridge.runEpoch})
}

// TestChatRuntimeEventBridge_MirrorsTurnBudgetWarning covers §6.4 落点 B:
// the 80% watermark event the agent loop emits must become a status-line
// watermark. 2026-09-28 起水位只作观测：源事件是 agent.turn.budget_warning，
// 模型侧不再有任何收尾注入。
func TestChatRuntimeEventBridge_MirrorsTurnBudgetWarning(t *testing.T) {
	const (
		sessionID = "session-turn-budget"
		turnID    = "turn-turn-budget"
		line      = "turn budget: step 240/300 · 32m/40m · tokens 62%"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)

	_, ok := bridge.TurnBudgetSnapshot()
	require.False(t, ok, "no watermark before the agent crosses the 80% limit")

	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", line, nil))

	snap, ok := bridge.TurnBudgetSnapshot()
	require.True(t, ok, "the 80% watermark event must be mirrored for the status line")
	require.Equal(t, line, snap.Line)
	require.Equal(t, "soft", snap.Level)
	require.InDelta(t, 0.8, snap.Ratio, 1e-9)
}

// TestChatRuntimeEventBridge_IgnoresNonBudgetEvents pins the filter: the legacy
// system_reminder.injected (kind=turn_budget) is no longer a source, other
// reminders are model-side cues, and a watermark event without a formatted line
// carries nothing user-visible.
func TestChatRuntimeEventBridge_IgnoresNonBudgetEvents(t *testing.T) {
	const (
		sessionID = "session-turn-budget-filter"
		turnID    = "turn-turn-budget-filter"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)

	for _, event := range []runtimeevents.Event{
		// 历史契约：软着陆提醒已取消，旧事件不再点亮状态行。
		{
			Type:      "system_reminder.injected",
			SessionID: sessionID,
			Payload:   map[string]interface{}{"turn_id": turnID, "kind": "turn_budget", "turn_budget_line": "turn budget: step 1/10"},
		},
		{
			Type:      "system_reminder.injected",
			SessionID: sessionID,
			Payload:   map[string]interface{}{"turn_id": turnID, "kind": "stop_hook", "body": "keep going"},
		},
		// Watermark event without a formatted line carries nothing user-visible.
		{
			Type:      chatRuntimeTurnBudgetWarningEvent,
			SessionID: sessionID,
			Payload:   map[string]interface{}{"turn_id": turnID, "turn_budget_line": "   "},
		},
	} {
		deliverTurnBudgetEvent(bridge, event)
		_, ok := bridge.TurnBudgetSnapshot()
		require.Falsef(t, ok, "event %v leaked into the status line", event.Type)
	}

	// ...and the real watermark still lands afterwards (the filter must not latch).
	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", "turn budget: step 240/300", nil))
	_, ok := bridge.TurnBudgetSnapshot()
	require.True(t, ok)
}

// TestChatRuntimeEventBridge_TurnBudgetIgnoresForeignTurn keeps the watermark
// scoped to the turn that actually reached its budget: an event still in flight
// from a retired turn must not decorate the new one.
func TestChatRuntimeEventBridge_TurnBudgetIgnoresForeignTurn(t *testing.T) {
	const (
		sessionID = "session-turn-budget-foreign"
		turnID    = "turn-current"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)

	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", "turn budget: step 240/300", map[string]interface{}{
		"turn_id": "turn-retired",
	}))

	_, ok := bridge.TurnBudgetSnapshot()
	require.False(t, ok, "a retired turn's watermark must not reach the status line")
}

// TestChatRuntimeEventBridge_TurnBudgetRequiresPrimarySession: the watermark is
// mirrored only for the session the bridge owns. Subagent turns share the bus
// but not the status row.
func TestChatRuntimeEventBridge_TurnBudgetRequiresPrimarySession(t *testing.T) {
	const (
		sessionID = "session-turn-budget-primary"
		turnID    = "turn-turn-budget-primary"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)

	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent("session-subagent", turnID, "soft", "turn budget: step 240/300", nil))

	_, ok := bridge.TurnBudgetSnapshot()
	require.False(t, ok, "another session's watermark must not decorate this status line")
}

// TestChatRuntimeEventBridge_BeginRunClearsTurnBudget: the watermark is
// per-run state, so a new turn never inherits the previous turn's row.
func TestChatRuntimeEventBridge_BeginRunClearsTurnBudget(t *testing.T) {
	const (
		sessionID = "session-turn-budget-reset"
		turnID    = "turn-turn-budget-reset"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)
	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", "turn budget: step 240/300", nil))
	_, ok := bridge.TurnBudgetSnapshot()
	require.True(t, ok)

	bridge.BeginRunKind(chatRunKindForeground)

	_, ok = bridge.TurnBudgetSnapshot()
	require.False(t, ok, "BeginRun must clear the previous run's watermark")
}

// TestChatInteractionCoordinator_TurnBudgetHintOnStatusLine covers the TUI
// presentation: the watermark is appended to the live status text and only
// while the run has a visible state.
func TestChatInteractionCoordinator_TurnBudgetHintOnStatusLine(t *testing.T) {
	const (
		sessionID = "session-turn-budget-line"
		turnID    = "turn-turn-budget-line"
		line      = "turn budget: step 240/300 · 32m/40m · tokens 62%"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)
	coord := &chatInteractionCoordinator{session: &ChatSession{RuntimeEventBridge: bridge}}

	// No watermark yet → the status text is untouched.
	model := &style.StatusLineModel{StateText: "Working for 12s (40m • esc to interrupt)"}
	require.Equal(t, model.StateText, coord.appendStatusHintsLocked(model).StateText)

	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", line, nil))

	model = &style.StatusLineModel{StateText: "Working for 12s (40m • esc to interrupt)"}
	got := coord.appendStatusHintsLocked(model)
	require.Equal(t, "Working for 12s (40m • esc to interrupt) · "+line, got.StateText)

	// An idle row (empty state text) must not grow a budget row.
	idle := &style.StatusLineModel{}
	require.True(t, strings.TrimSpace(coord.appendStatusHintsLocked(idle).StateText) == "")
}

// TestChatInteractionCoordinator_StatusHintsKeepDegradationSuffix pins the
// ordering of the two hints so the degradation warning stays adjacent to the
// run state and the budget line trails it.
func TestChatInteractionCoordinator_StatusHintsKeepDegradationSuffix(t *testing.T) {
	const (
		sessionID = "session-turn-budget-order"
		turnID    = "turn-turn-budget-order"
		line      = "turn budget: step 240/300"
	)
	bridge := newTurnBudgetTestBridge(t, sessionID, turnID)
	coord := &chatInteractionCoordinator{session: &ChatSession{RuntimeEventBridge: bridge}}
	deliverTurnBudgetEvent(bridge, turnBudgetWarningEvent(sessionID, turnID, "soft", line, nil))
	// Publish a non-zero degradation snapshot (evicted > 0) the same way the
	// bridge does when an event is shed.
	bridge.degradation.Store(&chatEventBridgeDegradation{Merged: 3, Evicted: 1, Dropped: 2})

	model := &style.StatusLineModel{StateText: "Running bash ..."}
	got := coord.appendStatusHintsLocked(model)

	require.Equal(t,
		"Running bash ... · ⚠ events degraded: merged=3 dropped=3 · "+line,
		got.StateText)
}
