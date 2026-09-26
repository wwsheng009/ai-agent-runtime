package toolbroker

import (
	"sort"
	"strings"
)

// brokerToolArgKeys declares, per broker tool, every argument key the broker
// consumes. The table is derived from the actual argument reads in execute() and
// executeSupervisionTool() (including the keys read by their parse helpers), so
// it stays a factual statement of the tool contract instead of a wish list.
//
// A key the caller sends that is missing here is reported back through
// annotateIgnoredBrokerToolArgs as ignored_args instead of being dropped in
// silence. Silent dropping is the failure class behind the delegation bugs this
// package already fixed: `goal` reached spawn_agent (prompt lost, child had no
// task) and `tools_whitelist` reached tools that never restrict their tool
// surface. spawn_agent keeps its hard fail-closed allowlist
// (spawnAgentToolArgKeys); every other tool reports the dropped keys, because
// models legitimately add harmless extra keys that must not fail the call.
var brokerToolArgKeys = map[string][]string{
	ToolAskUserQuestion:      {"prompt", "required", "suggestions"},
	ToolEnterPlanMode:        {"plan_path", "plan_write_paths"},
	ToolExitPlanMode:         {"decision", "notes"},
	ToolPlanReview:           {"plan_id", "plan_path", "version", "compare_version"},
	ToolBackgroundTask:       {"command", "cwd", "priority", "restart_policy", "startup_acceptance", "timeout_sec"},
	ToolTaskOutput:           {"job_id", "limit", "offset"},
	ToolSpawnAgent:           spawnAgentToolArgKeys,
	ToolListAgents:           {"include_closed", "parent_session_id", "path_prefix"},
	ToolSendMessage:          {"id", "message", "session_id", "target"},
	ToolFollowupTask:         {"id", "message", "session_id", "target"},
	ToolSendInput:            {"id", "interrupt", "message", "session_id"},
	ToolResolveAgentApproval: {"allow", "id", "patched_args", "request_id", "session_id"},
	ToolWaitAgent:            {"after_seq", "id", "ids", "session_id", "session_ids", "timeout_ms"},
	ToolReadAgentEvents:      {"after_seq", "id", "limit", "session_id", "view", "wait_ms"},
	ToolCloseAgent:           {"id", "session_id"},
	ToolResumeAgent:          {"id", "session_id"},
	ToolApplyAgentWorktree:   {"force", "id", "keep", "paths", "session_id"},
	ToolDiscardAgentWorktree: {"id", "session_id"},
	ToolSpawnTeam: {
		"allow_existing", "auto_start", "lead_session_id", "max_teammates", "max_writers",
		"status", "strategy", "tasks", "team_id", "teammates", "workspace_id",
	},
	ToolWaitTeam:          {"after_seq", "limit", "require_summary", "team_id", "timeout_ms"},
	ToolSendTeamMessage:   {"body", "kind", "metadata", "task_id", "team_id", "to_agent"},
	ToolReadMailboxDigest: {"agent_id", "limit", "mark_read", "team_id"},
	ToolReadTaskSpec:      {"task_id", "team_id"},
	ToolReadTaskContext: {
		"context_budget", "include_dependencies", "include_mailbox",
		"mailbox_limit", "mark_read", "task_id", "team_id",
	},
	ToolReportTaskOutcome: {
		"auto_replan", "blocker", "handoff_to", "notify_lead",
		"result_ref", "summary", "task_id", "task_status", "team_id",
	},
	ToolBlockCurrentTask: {
		"auto_replan", "blocker", "handoff_to", "notify_lead",
		"summary", "task_id", "task_status", "team_id",
	},
	ToolSupervisionSnapshot:    {"after_seq", "include_resolved", "limit"},
	ToolSupervisionDescendants: {"after_seq", "health", "include_results", "include_terminal", "limit", "mode"},
	ToolSubagentStatus:         {"after_seq", "health", "include_digest", "include_resolved", "include_results", "include_terminal", "limit", "mode"},
	ToolSubagentInspectTask:    {"agent", "child_session_id", "id", "include_status", "limit", "max_chars", "offset", "sections", "session_id", "target", "task_id"},
	ToolReadAgentResult:        {"id", "limit", "max_chars", "offset", "sections", "task_id"},
	ToolAckLifecycle:           {"notification_id", "decision", "note", "reason", "state", "until", "expected_version"},
	ToolControlDescendant:      {"notification_id", "action", "reason", "cascade", "expected_version"},
}

// brokerIgnoredArgHint explains what to use instead of a key the addressed tool
// does not implement. Empty means the generic note is enough.
func brokerIgnoredArgHint(toolName, key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "tools_whitelist", "tool_whitelist", "tools", "whitelist", "allowed_tools":
		return "no broker tool restricts its tool surface this way; spawn_subagents task entries take tools_whitelist"
	case "message", "goal", "task", "prompt", "instruction", "instructions",
		"task_description", "objective", "goal_text", "content", "text", "description":
		return "this tool carries no task prompt; spawn_agent (message, alias goal/task/prompt), send_message, followup_task and send_input do"
	case "read_only", "permission_mode", "difficulty", "difficulty_rationale",
		"task_type", "task_subject", "isolation",
		"fork_context", "fork_turns", "completion_requirement", "provider", "model",
		"reasoning_effort", "thinking_effort":
		return "delegation options are accepted by spawn_agent, spawn_subagents and spawn_team"
	case "job_id", "job_ref":
		return "task_output addresses a background job by job_id"
	case "task_id", "task_status":
		return "task-scoped tools are read_task_spec, read_task_context, report_task_outcome, block_current_task and send_team_message"
	case "team_id", "to_agent":
		return "team-scoped tools are spawn_team, wait_team, send_team_message, read_mailbox_digest, read_task_spec and read_task_context"
	case "notification_id":
		return "supervision notifications belong to subagent_status(include_digest=true), subagent_ack_lifecycle and subagent_control"
	case "agent_id":
		return "read_mailbox_digest addresses a mailbox by agent_id; child sessions use id/session_id"
	case "target":
		return "send_message and followup_task address a child by target/id/session_id"
	case "keep":
		return "apply_agent_worktree is the only tool that accepts keep"
	case "interrupt":
		return "send_input is the only tool that accepts interrupt"
	case "allow":
		return "resolve_agent_approval is the only tool that accepts allow"
	default:
		return ""
	}
}

// annotateIgnoredBrokerToolArgs reports the argument keys a tool did not consume.
// It is advisory on purpose: the call already succeeded, so the goal is to make a
// dropped intent visible to the caller (and to the runtime logs) rather than to
// fail a call that may still be useful. The note is merged into next_action,
// which the agent runtime renders back to the model, so a mistyped key is
// corrected on the next turn instead of being repeated forever.
func annotateIgnoredBrokerToolArgs(toolName string, args map[string]interface{}, metadata map[string]interface{}) map[string]interface{} {
	if len(args) == 0 {
		return metadata
	}
	allowed, ok := brokerToolArgKeys[normalizeToolName(toolName)]
	if !ok {
		return metadata
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	ignored := make([]string, 0, len(args))
	for key := range args {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := allowedSet[key]; !ok {
			ignored = append(ignored, key)
		}
	}
	if len(ignored) == 0 {
		return metadata
	}
	sort.Strings(ignored)

	parts := make([]string, 0, len(ignored))
	for _, key := range ignored {
		if hint := brokerIgnoredArgHint(toolName, key); hint != "" {
			parts = append(parts, key+" ("+hint+")")
			continue
		}
		parts = append(parts, key)
	}
	note := "ignored unsupported arguments: " + strings.Join(parts, ", ") +
		"; remove them or call the tool that implements them"

	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["ignored_args"] = ignored
	if existing, _ := metadata["next_action"].(string); strings.TrimSpace(existing) != "" {
		note = strings.TrimSpace(existing) + " | " + note
	}
	metadata["next_action"] = note
	return metadata
}
