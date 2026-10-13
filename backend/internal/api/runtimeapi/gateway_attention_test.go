package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestGatewayAttentionTurnSuspendedProjectsWaiting pins the §6.12 parked edge
// for the API host (docs/gateway/08-notifications.md §3): turn.suspended →
// attention=waiting + the shared activity line; turn.resumed clears it.
func TestGatewayAttentionTurnSuspendedProjectsWaiting(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnStarted, SessionID: "s5"})
	bus.Publish(runtimeevents.Event{Type: runtimeevents.EventTurnSuspended, SessionID: "s5"})
	if st := handler.testGatewayState("s5"); st.Attention != gatewayagent.AttentionWaiting ||
		st.State != wire.SessionStateRunning || st.StatusText != gatewayagent.StatusTextWaitingSubagents {
		t.Fatalf("suspended: %+v", st)
	}
	bus.Publish(runtimeevents.Event{Type: runtimeevents.EventTurnResumed, SessionID: "s5"})
	if st := handler.testGatewayState("s5"); st.Attention != "" {
		t.Fatalf("resumed: %+v", st)
	}
}

// TestGatewayAttentionParkedTurnDoneKeepsWaiting pins the run-end carve-out:
// agent.turn.finished must not project completed while the durable parked
// record still exists, and must go back to completed once it is gone.
func TestGatewayAttentionParkedTurnDoneKeepsWaiting(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	handler.SetSubagentBatchStore(newAPISupervisionBatchStore(t))
	ctx := context.Background()
	const sessionID = "s6-parked"
	require.NoError(t, handler.getSubagentBatchStore().ParkTurnSuspension(ctx, &subagentbatch.TurnSuspension{
		TurnID:        "turn-s6",
		SessionID:     sessionID,
		RootScopeID:   sessionID,
		ObligationIDs: []string{"batch-1"},
		ResumeQueue:   []string{"batch-1"},
	}))

	bus := handler.getRuntimeEventBus()
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnStarted, SessionID: sessionID})
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: sessionID})
	st := handler.testGatewayState(sessionID)
	require.Equal(t, gatewayagent.AttentionWaiting, st.Attention)
	require.Equal(t, wire.SessionStateRunning, st.State)
	require.Equal(t, gatewayagent.StatusTextWaitingSubagents, st.StatusText)
	require.True(t, st.Busy, "a parked turn must not be projected as finished")

	require.NoError(t, handler.getSubagentBatchStore().ClearTurnSuspension(ctx, sessionID, "turn-s6"))
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: sessionID})
	st = handler.testGatewayState(sessionID)
	require.Equal(t, gatewayagent.AttentionCompleted, st.Attention)
	require.False(t, st.Busy)
}

// st returns the shared session-state projection maintained by the handler.
func (h *Handler) testGatewayState(sessionID string) gatewayagent.SessionState {
	return h.gatewayStateStore().Get(sessionID)
}

func TestGatewayAttentionProjection(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetGatewayController(&fakeGatewayController{})
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	// Turn start: busy, running, attention cleared.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnStarted, SessionID: "s1"})
	if st := handler.testGatewayState("s1"); !st.Busy || st.Attention != "" || st.State != wire.SessionStateRunning {
		t.Fatalf("turn start: %+v", st)
	}

	// Approval pending: high-priority attention + review state.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalRequested, SessionID: "s1"})
	if st := handler.testGatewayState("s1"); st.Attention != gatewayagent.AttentionApproval || st.State != wire.SessionStateReview {
		t.Fatalf("approval: %+v", st)
	}

	// Approval resolved: attention cleared and the state returns to running.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalResolved, SessionID: "s1"})
	if st := handler.testGatewayState("s1"); st.Attention != "" || !st.Busy || st.State != wire.SessionStateRunning {
		t.Fatalf("approval resolved: %+v", st)
	}

	// Turn finished: completed, not busy.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s1"})
	if st := handler.testGatewayState("s1"); st.Attention != gatewayagent.AttentionCompleted || st.Busy || st.State != wire.SessionStateCompleted {
		t.Fatalf("turn done: %+v", st)
	}

	// Session end clears the projection entirely.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: "s1"})
	if st := handler.testGatewayState("s1"); st.Attention != "" || st.Busy || st.State != "" {
		t.Fatalf("session end: %+v", st)
	}
}

func TestGatewayAttentionQuestionAndInterrupt(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventQuestionAsked, SessionID: "s2"})
	if st := handler.testGatewayState("s2"); st.Attention != gatewayagent.AttentionQuestion || st.State != wire.SessionStateWaiting {
		t.Fatalf("question: %+v", st)
	}
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventQuestionAnswered, SessionID: "s2"})
	if st := handler.testGatewayState("s2"); st.Attention != "" {
		t.Fatalf("question answered: %+v", st)
	}
	// Interrupt keeps the row visible with an interrupted state.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionInterrupted, SessionID: "s2"})
	if st := handler.testGatewayState("s2"); st.Attention != "" || st.Busy || st.State != wire.SessionStateInterrupted {
		t.Fatalf("interrupted: %+v", st)
	}
}

func TestEnableGatewayAttentionIsIdempotent(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	handler.EnableGatewayAttention()
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s3"})
	if st := handler.testGatewayState("s3"); st.Attention != gatewayagent.AttentionCompleted {
		t.Fatalf("after double enable: %+v", st)
	}
}

// TestGatewaySurfaceActivityProjection covers the fine-grained activity axis:
// tool / llm events become running/answering with a one-line status text.
func TestGatewaySurfaceActivityProjection(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnStarted, SessionID: "s4"})
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventToolStarted, SessionID: "s4", ToolName: "shell"})
	if st := handler.testGatewayState("s4"); st.State != wire.SessionStateRunning || st.StatusText != "Running shell" {
		t.Fatalf("tool started: %+v", st)
	}
	// Tool finished: the name is stale, generic running remains.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventToolFinished, SessionID: "s4", ToolName: "shell"})
	if st := handler.testGatewayState("s4"); st.State != wire.SessionStateRunning || st.StatusText != "" {
		t.Fatalf("tool finished: %+v", st)
	}
	// LLM request: answering.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventLLMRequestStart, SessionID: "s4"})
	if st := handler.testGatewayState("s4"); st.State != wire.SessionStateAnswering || st.StatusText != "Generating response" {
		t.Fatalf("llm request: %+v", st)
	}
	// Retry: running.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventLLMRetry, SessionID: "s4"})
	if st := handler.testGatewayState("s4"); st.State != wire.SessionStateRunning || st.StatusText != "Retrying" {
		t.Fatalf("llm retry: %+v", st)
	}
	// Turn end clears the activity text but keeps the terminal state.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s4"})
	if st := handler.testGatewayState("s4"); st.State != wire.SessionStateCompleted || st.StatusText != "" || st.Busy {
		t.Fatalf("turn done: %+v", st)
	}
}

// TestGatewayAttentionEmitsNotify verifies the host also emits a NOTIFY event
// (docs/gateway/08-notifications.md §4.1) alongside the SESSION_UPDATE state.
func TestGatewayAttentionEmitsNotify(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	controller := &fakeGatewayController{}
	handler.SetGatewayController(controller)
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalRequested, SessionID: "s1"})
	if len(controller.notifies) != 1 {
		t.Fatalf("notifies = %+v", controller.notifies)
	}
	n := controller.notifies[0]
	if n.SessionID != "s1" || n.Kind != wire.NotifyKindApproval || n.Priority != wire.NotifyPriorityHigh {
		t.Fatalf("notify = %+v", n)
	}

	// A resolved approval clears attention and emits no NOTIFY event.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalResolved, SessionID: "s1"})
	if len(controller.notifies) != 1 {
		t.Fatalf("resolution should not notify: %+v", controller.notifies)
	}

	// Turn completion is a normal-priority event.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s1"})
	if len(controller.notifies) != 2 {
		t.Fatalf("turn done notifies = %+v", controller.notifies)
	}
	if got := controller.notifies[1]; got.Kind != wire.NotifyKindTurnCompleted || got.Priority != wire.NotifyPriorityNormal {
		t.Fatalf("turn-done notify = %+v", got)
	}
}
