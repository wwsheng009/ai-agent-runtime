package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestLegacyInteractionWaitsForEarlierRuntimeActorAction(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "ordering-session"},
	}
	coordinator := newChatInteractionCoordinator(session)
	session.Interaction = coordinator
	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("UI actor was not created")
	}

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	epoch := bridge.runEpoch
	deltaStarted := make(chan struct{})
	releaseDelta := make(chan struct{})
	approvalStarted := make(chan struct{})
	bridge.writeDelta = func(string) {
		close(deltaStarted)
		<-releaseDelta
	}
	bridge.askApproval = func(*runtimechat.ApprovalRequest, []string) (chatApprovalAnswer, error) {
		close(approvalStarted)
		return chatApprovalAnswer{Allowed: false}, nil
	}
	t.Cleanup(func() {
		select {
		case <-releaseDelta:
		default:
			close(releaseDelta)
		}
		coordinator.closeUIActor()
	})

	bridge.handleQueuedEvent(chatRuntimeQueuedEvent{event: runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "ordering-session",
		Payload: map[string]interface{}{
			"delta": "earlier assistant delta",
		},
	}, epoch: epoch})
	select {
	case <-deltaStarted:
	case <-time.After(time.Second):
		t.Fatal("earlier runtime action did not enter the UI actor")
	}

	interactionDone := make(chan struct{})
	go func() {
		defer close(interactionDone)
		bridge.handleQueuedEvent(chatRuntimeQueuedEvent{event: runtimeevents.Event{
			Type:      runtimechat.EventApprovalRequested,
			SessionID: "ordering-session",
			Payload: map[string]interface{}{
				"request_id": "approval-after-delta",
				"tool_name":  "shell",
				"reason":     "ordering test",
			},
		}, epoch: epoch})
	}()

	select {
	case <-approvalStarted:
		t.Fatal("later approval bypassed the earlier runtime actor action")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseDelta)

	select {
	case <-approvalStarted:
	case <-time.After(time.Second):
		t.Fatal("approval did not resume after the earlier actor action completed")
	}
	select {
	case <-interactionDone:
	case <-time.After(time.Second):
		t.Fatal("approval event did not complete")
	}
}
