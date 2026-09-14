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

// localAgentReclaimReportWindow bounds how long one eviction pass is remembered
// by the publication guard. One pass can be observed by several callers in the
// same host (spawn gate, projection sweep, periodic reconcile); only the first
// observation may reach the bus and the durable session stream, otherwise the
// identical summary is replayed to the operator once per observer.
const localAgentReclaimReportWindow = 5 * time.Minute

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
	now := time.Now().UTC()
	if !r.claimLocalAgentReclaimReport(localAgentReclaimReportKey(sessionID, payload), now) {
		// The same pass was already advertised: a second identical line would
		// make /runtime/events and the transcript show one eviction twice.
		return
	}
	event := runtimeevents.Event{
		Type:      agentcontrol.EventAgentReclaimed,
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: now,
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

// localAgentReclaimReportKey identifies one eviction report by the eviction it
// describes — the parent stream plus the closed children — not by the observer
// that happened to notice it (source is deliberately excluded) and not by the
// counters of that particular observation.
//
// Counters are volatile while several observers sweep concurrently: a second
// observer holding a pre-close listing re-selects children the first one
// already closed, so the very same eviction can arrive first as
// `reclaimed=1 reclaimed_rows=1` and then as `reclaimed=2 reclaimed_rows=1`
// (the extra decision is the 0-row no-op above). Keying on summary/reclaimed
// therefore handed each re-observation its own key and the 5-minute guard let
// the same cleanup through as several transcript lines.
//
// Reclaiming rows leaves them terminal, so a genuinely later pass closes
// different children (or the same children re-created) and keeps its own key;
// only a re-observation of the same child set collapses.
func localAgentReclaimReportKey(sessionID string, payload map[string]interface{}) string {
	return strings.Join([]string{
		strings.TrimSpace(sessionID),
		localAgentReclaimStringPayload(payload, "root_session_id"),
		strings.Join(localAgentReclaimStringsPayload(payload, "agent_paths"), ","),
		fmt.Sprintf("%d", localAgentReclaimIntPayload(payload, "truncated")),
	}, "|")
}

// claimLocalAgentReclaimReport reports whether this observer may publish the
// pass. The first caller for key wins and a repeat inside
// localAgentReclaimReportWindow is dropped, so one eviction is advertised once
// even when the spawn gate, the projection sweep and the periodic reconcile all
// observe the same outcome.
func (r *localActorRegistry) claimLocalAgentReclaimReport(key string, now time.Time) bool {
	if r == nil || strings.TrimSpace(key) == "" {
		return true
	}
	r.reclaimReportMu.Lock()
	defer r.reclaimReportMu.Unlock()
	if r.reclaimReportKeys == nil {
		r.reclaimReportKeys = make(map[string]time.Time, 4)
	}
	for existing, seen := range r.reclaimReportKeys {
		if now.Sub(seen) > localAgentReclaimReportWindow {
			delete(r.reclaimReportKeys, existing)
		}
	}
	if seen, ok := r.reclaimReportKeys[key]; ok && now.Sub(seen) <= localAgentReclaimReportWindow {
		return false
	}
	r.reclaimReportKeys[key] = now
	return true
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
