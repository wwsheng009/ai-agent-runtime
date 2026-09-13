package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// localAgentReclaimEventLimit bounds how many retained bus events the /debug
// tally scans. The tally is a diagnostic ("did anything get evicted lately?"),
// not an audit log: the durable registry rows (reclaimed:<reason>) and the
// session event stream remain the source of truth.
const localAgentReclaimEventLimit = 512

// recordLocalAgentReclaim mirrors one quota eviction pass onto the CLI runtime
// event bus and, best effort, into the durable parent session event stream
// (plan §P2-8 方案 4). It is a no-op when nothing was reclaimed, so a gate that
// only rejected the spawn never publishes an empty event.
func (r *localActorRegistry) recordLocalAgentReclaim(ctx context.Context, parentSessionID, rootSessionID, source string, outcome agentcontrol.ReclaimOutcome) {
	if r == nil || r.Host == nil || outcome.Reclaimed() == 0 {
		return
	}
	payload := agentcontrol.ReclaimEventPayload(source, outcome)
	if root := strings.TrimSpace(rootSessionID); root != "" {
		payload["root_session_id"] = root
	}
	sessionID := strings.TrimSpace(parentSessionID)
	event := runtimeevents.Event{
		Type:      agentcontrol.EventAgentReclaimed,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	if bus := r.Host.EventBus; bus != nil {
		bus.Publish(event)
	}
	if store := r.Host.EventStore; store != nil && sessionID != "" {
		// Durable mirror: /runtime/events and the frontend subagent drill-down
		// keep the eviction after the in-memory bus is rotated away. Failures
		// stay silent (the registry wake stream is the authoritative channel).
		_, _ = store.AppendEvent(ctx, event)
	}
}

// localAgentReclaimSummary renders the P2-8 reclaim tally for `/debug` and
// `/agents panel`, e.g.
//
//	reclaim events=2 rows=3 failed=0 last=2026-09-13T05:10:00Z source=spawn_gate reasons=session_terminal
//
// `reclaim events=0` means “no eviction observed in this process since
// startup”, which is different from an unreadable bus (`reclaim=unavailable`).
func (h *localChatRuntimeHost) localAgentReclaimSummary() string {
	if h == nil || h.EventBus == nil {
		return "reclaim=unavailable"
	}
	events := 0
	rows := int64(0)
	failed := 0
	source := ""
	var last time.Time
	reasons := make([]string, 0, 2)
	for _, event := range h.EventBus.Recent(localAgentReclaimEventLimit) {
		if strings.TrimSpace(event.Type) != agentcontrol.EventAgentReclaimed {
			continue
		}
		events++
		rows += localAgentReclaimIntPayload(event.Payload, "rows")
		failed += int(localAgentReclaimIntPayload(event.Payload, "failed"))
		if payloadSource := strings.TrimSpace(localAgentReclaimStringPayload(event.Payload, "source")); payloadSource != "" {
			source = payloadSource
		}
		if !event.Timestamp.IsZero() {
			last = event.Timestamp.UTC()
		}
		for _, reason := range localAgentReclaimStringsPayload(event.Payload, "reasons") {
			if !localAgentReclaimContains(reasons, reason) {
				reasons = append(reasons, reason)
			}
		}
	}
	if events == 0 {
		return "reclaim events=0"
	}
	parts := []string{fmt.Sprintf("reclaim events=%d", events)}
	if rows > 0 {
		parts = append(parts, fmt.Sprintf("rows=%d", rows))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("failed=%d", failed))
	}
	if !last.IsZero() {
		parts = append(parts, "last="+last.Format(time.RFC3339))
	}
	if source != "" {
		parts = append(parts, "source="+source)
	}
	if len(reasons) > 0 {
		parts = append(parts, "reasons="+strings.Join(reasons, ","))
	}
	return strings.Join(parts, " ")
}

func localAgentReclaimIntPayload(payload map[string]interface{}, key string) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func localAgentReclaimStringPayload(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func localAgentReclaimStringsPayload(payload map[string]interface{}, key string) []string {
	if payload == nil {
		return nil
	}
	switch value := payload[key].(type) {
	case []string:
		return value
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	}
	return nil
}

func localAgentReclaimContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
