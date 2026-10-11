package runtimeapi

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestGatewayAttentionProjection(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetGatewayController(&fakeGatewayController{})
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	// Turn start: busy, attention cleared.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnStarted, SessionID: "s1"})
	if st := handler.gatewaySessionStateFor("s1"); !st.busy || st.attention != "" {
		t.Fatalf("turn start: %+v", st)
	}

	// Approval pending: high-priority attention.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalRequested, SessionID: "s1"})
	if st := handler.gatewaySessionStateFor("s1"); st.attention != gatewayAttentionApproval {
		t.Fatalf("approval: %+v", st)
	}

	// Approval resolved: attention cleared, still busy.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalResolved, SessionID: "s1"})
	if st := handler.gatewaySessionStateFor("s1"); st.attention != "" || !st.busy {
		t.Fatalf("approval resolved: %+v", st)
	}

	// Turn finished: completed, not busy.
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s1"})
	if st := handler.gatewaySessionStateFor("s1"); st.attention != gatewayAttentionCompleted || st.busy {
		t.Fatalf("turn done: %+v", st)
	}

	// Session end clears the projection entirely.
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: "s1"})
	if st := handler.gatewaySessionStateFor("s1"); st.attention != "" || st.busy {
		t.Fatalf("session end: %+v", st)
	}
}

func TestGatewayAttentionQuestionAndInterrupt(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	bus := handler.getRuntimeEventBus()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventQuestionAsked, SessionID: "s2"})
	if st := handler.gatewaySessionStateFor("s2"); st.attention != gatewayAttentionQuestion {
		t.Fatalf("question: %+v", st)
	}
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventQuestionAnswered, SessionID: "s2"})
	if st := handler.gatewaySessionStateFor("s2"); st.attention != "" {
		t.Fatalf("question answered: %+v", st)
	}
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionInterrupted, SessionID: "s2"})
	if st := handler.gatewaySessionStateFor("s2"); st.attention != "" || st.busy {
		t.Fatalf("interrupted: %+v", st)
	}
}

func TestEnableGatewayAttentionIsIdempotent(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.EnableGatewayAttention()
	handler.EnableGatewayAttention()
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "s3"})
	if st := handler.gatewaySessionStateFor("s3"); st.attention != gatewayAttentionCompleted {
		t.Fatalf("after double enable: %+v", st)
	}
}
