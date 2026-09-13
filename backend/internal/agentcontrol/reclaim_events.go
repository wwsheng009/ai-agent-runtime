package agentcontrol

import (
	"strings"
)

// EventAgentReclaimed is the product runtime event published after one quota
// eviction pass actually closed at least one child subtree (plan §P2-8 方案 4:
// “回收动作写入事件流（agent.reclaimed），供前端/CLI 展示”).
//
// The durable mirror stays on the registry row / wake stream
// (`reclaimed:<reason>`, see ReclaimDecision.EventKind), so a parent reading
// read_agent_events keeps the authoritative per-child explanation; this product
// event is the compact, session-scoped summary that the parent session stream
// (frontend `/runtime/events`, subagent drill-down) and `/debug` render.
const EventAgentReclaimed = "agent.reclaimed"

// Reclaim sources distinguish who triggered the eviction. The spawn gate drops
// a child to admit a new one (the parent is already at the thread limit), while
// the manual entry point is the operator-driven `/agents cleanup` command.
const (
	ReclaimSourceSpawnGate     = "spawn_gate"
	ReclaimSourceManualCleanup = "manual_cleanup"
	// ReclaimSourceReconcile is the periodic sweep (plan §P2-9 方案 3): the
	// host runs the same decision path on every reconcile tick, so a terminal
	// or idle child releases its slot without waiting for the next spawn.
	ReclaimSourceReconcile = "reconcile"
)

// reclaimEventAgentLimit bounds how many evicted paths/sessions travel in one
// payload. A wide subtree is summarised by counters plus the first few rows so
// the event stays readable and small; the durable rows keep every detail.
const reclaimEventAgentLimit = 8

// ReclaimEventPayload renders the product-event payload for one eviction pass.
//
// Only successfully closed decisions are listed: a failed close is counted
// (and reported once through `error`) because that child is still registered
// and must not be advertised as reclaimed. Keys are stable so both hosts and
// the frontend can rely on them:
//
//	source, reclaimed, summary, rows?, failed?, error?,
//	reasons?, agent_paths?, agent_path?, session_ids?, session_id?, truncated?
func ReclaimEventPayload(source string, outcome ReclaimOutcome) map[string]interface{} {
	source = strings.TrimSpace(source)
	if source == "" {
		source = ReclaimSourceSpawnGate
	}
	payload := map[string]interface{}{
		"source":    source,
		"reclaimed": outcome.Reclaimed(),
		"summary":   outcome.Summary(),
	}
	if outcome.Rows > 0 {
		payload["rows"] = outcome.Rows
	}
	if outcome.Failed > 0 {
		payload["failed"] = outcome.Failed
	}
	if first := strings.TrimSpace(outcome.FirstError); first != "" {
		payload["error"] = first
	}

	reasons := make([]string, 0, len(outcome.Decisions))
	paths := make([]string, 0, len(outcome.Decisions))
	sessions := make([]string, 0, len(outcome.Decisions))
	for _, decision := range outcome.Decisions {
		if reason := strings.TrimSpace(decision.Reason); reason != "" && !containsAgentString(reasons, reason) {
			reasons = append(reasons, reason)
		}
		if path := strings.TrimSpace(decision.AgentPath); path != "" {
			paths = append(paths, path)
		}
		if sessionID := strings.TrimSpace(decision.SessionID); sessionID != "" {
			sessions = append(sessions, sessionID)
		}
	}
	if hidden := len(paths) - reclaimEventAgentLimit; hidden > 0 {
		payload["truncated"] = hidden
		paths = paths[:reclaimEventAgentLimit]
	}
	if hidden := len(sessions) - reclaimEventAgentLimit; hidden > 0 {
		sessions = sessions[:reclaimEventAgentLimit]
	}
	if len(reasons) > 0 {
		payload["reasons"] = reasons
	}
	if len(paths) > 0 {
		payload["agent_paths"] = paths
		// agent_path mirrors the first evicted child so single-child consumers
		// (and existing renderers keyed on `agent_path`) keep working.
		payload["agent_path"] = paths[0]
	}
	if len(sessions) > 0 {
		payload["session_ids"] = sessions
		payload["session_id"] = sessions[0]
	}
	return payload
}

func containsAgentString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
