package toolbroker

import (
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// BuildAgentEventsResult projects raw runtime events into the stable
// subagent_events response shape. Payloads are deep-cloned so callers and
// retention code cannot mutate each other through shared maps/slices.
func BuildAgentEventsResult(sessionID string, events []runtimeevents.Event) *AgentEventsResult {
	result := &AgentEventsResult{
		SessionID: strings.TrimSpace(sessionID),
		Count:     len(events),
	}
	if len(events) == 0 {
		return FinalizeAgentEventsResult(result)
	}
	items := make([]AgentEventItem, 0, len(events))
	var latestSeq int64
	for _, event := range events {
		cloned := runtimeevents.CloneEvent(event)
		item := AgentEventItem{
			Type:      cloned.Type,
			TraceID:   cloned.TraceID,
			SessionID: cloned.SessionID,
			ToolName:  cloned.ToolName,
			AgentName: cloned.AgentName,
			Timestamp: cloned.Timestamp,
			Payload:   cloned.Payload,
		}
		if seq := agentEventSeqFromPayload(cloned.Payload); seq > 0 {
			item.Seq = seq
			if seq > latestSeq {
				latestSeq = seq
			}
		}
		items = append(items, item)
	}
	result.Events = items
	result.LatestSeq = latestSeq
	return FinalizeAgentEventsResult(result)
}

// BuildAgentEventsTimeoutResult marks a no-event wait window as timed out after
// the normal event projection, so timeout-specific next_action wins.
func BuildAgentEventsTimeoutResult(sessionID string, events []runtimeevents.Event) *AgentEventsResult {
	result := BuildAgentEventsResult(sessionID, events)
	result.TimedOut = true
	result.NextAction = ""
	return FinalizeAgentEventsResult(result)
}

func agentEventSeqFromPayload(payload map[string]interface{}) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload["seq"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
