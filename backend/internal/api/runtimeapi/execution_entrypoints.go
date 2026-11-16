package runtimeapi

import "github.com/wwsheng009/ai-agent-runtime/internal/chat"

// ExecutionEntrypointSnapshot is the shared host diagnostic for which execution
// core each entrypoint actually uses. It is exported so /health and the runtime
// status endpoint cannot drift.
func ExecutionEntrypointSnapshot() map[string]interface{} {
	return map[string]interface{}{
		"/api/agent/chat": map[string]interface{}{
			"core":                   chat.SessionActorRuntimeCore(),
			"fallback_core":          "stateless_direct",
			"durable_state":          true,
			"cross_request_approval": true,
			"eligible_scope":         "enable_react plus a last user message; multi-message replay and Team/Task context run on the actor, while enable_react=false planning/planned-subagent/patch requests remain on the stateless direct orchestrator",
		},
		"/api/runtime/sessions/{id}/runtime/commands": chat.SessionActorRuntimeCore(),
	}
}
