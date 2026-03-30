package skills

import (
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func buildSessionRuntimeEventView(event runtimeevents.Event) map[string]interface{} {
	return map[string]interface{}{
		"type":       event.Type,
		"trace_id":   event.TraceID,
		"agent_name": event.AgentName,
		"session_id": event.SessionID,
		"tool_name":  event.ToolName,
		"payload":    event.Payload,
		"timestamp":  event.Timestamp,
		"provenance": summarizeSingleRuntimeEventProvenance(event),
	}
}

func buildSessionRuntimeEventViews(events []runtimeevents.Event) []map[string]interface{} {
	views := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		views = append(views, buildSessionRuntimeEventView(event))
	}
	return views
}

func summarizeSingleRuntimeEventProvenance(event runtimeevents.Event) map[string]interface{} {
	summary := runtimeevents.ProvenanceView{
		ProfileResourceKinds: make(map[string]int),
	}
	runtimeevents.ApplyProvenanceEventForAPI(&summary, event)
	return buildProvenanceSummaryFromView(summary)
}

