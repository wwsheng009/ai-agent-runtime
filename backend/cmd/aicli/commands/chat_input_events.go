package commands

import (
	"context"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

const (
	chatEventInputQueueDetected  = "input.queue.detected"
	chatEventInputQueueDiscarded = "input.queue.discarded"
	chatEventInputQueueDrained   = "input.queue.drained"
	chatInputQueueAgentName      = "aicli-input-queue"
)

func publishLocalChatDiagnosticEvent(session *ChatSession, eventType string, payload map[string]interface{}) {
	if session == nil || session.LocalRuntimeHost == nil || strings.TrimSpace(eventType) == "" {
		return
	}
	sessionID := ""
	if session.RuntimeSession != nil {
		sessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if sessionID == "" {
		return
	}
	event := runtimeevents.Event{
		Type:      strings.TrimSpace(eventType),
		AgentName: chatInputQueueAgentName,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	if session.LocalRuntimeHost.EventStore != nil {
		if seq, err := session.LocalRuntimeHost.EventStore.AppendEvent(context.Background(), event); err == nil {
			if event.Payload == nil {
				event.Payload = map[string]interface{}{}
			}
			event.Payload["seq"] = seq
		}
	}
	if session.LocalRuntimeHost.EventBus != nil {
		session.LocalRuntimeHost.EventBus.Publish(event)
	}
}
