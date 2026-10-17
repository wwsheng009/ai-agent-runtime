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
// package already fixed: `goal` reached subagent_spawn (prompt lost, child had no
// task) and `tools_whitelist` reached tools that never restrict their tool
// surface. subagent_spawn keeps its hard fail-closed allowlist
// (spawnAgentToolArgKeys); every other tool reports the dropped keys, because
// models legitimately add harmless extra keys that must not fail the call.
var brokerToolArgKeys = map[string][]string{
	ToolAskUserQuestion:         {"prompt", "required", "suggestions"},
	ToolPlanEnter:               {"plan_path", "plan_write_paths"},
	ToolPlanExit:                {"decision", "notes"},
	ToolPlanReview:              {"plan_id", "plan_path", "version", "compare_version"},
	ToolJobStart:                {"command", "cwd", "priority", "restart_policy", "startup_acceptance", "timeout_sec", "interactive", "tty", "cols", "rows"},
	ToolJobOutput:               {"job_id", "limit", "offset", "timeout_ms", "wait"},
	ToolJobCancel:               {"job_id", "reason", "task_id"},
	ToolJobMonitor:              {"cancel", "check_after_ms", "job_id", "max_duration_ms", "task_id"},
	ToolJobControl:              {"action", "job_id", "reason", "task_id"},
	ToolJobInput:                {"job_id", "task_id", "session_id", "chars", "input", "yield_time_ms", "close_stdin"},
	ToolSubagentSpawn:           spawnAgentToolArgKeys,
	ToolSubagentList:            {"include_closed", "path_prefix"},
	ToolSubagentSend:            {"id", "message", "session_id", "target"},
	ToolSubagentFollowup:        {"id", "message", "session_id", "target"},
	ToolSubagentInput:           {"id", "interrupt", "message", "session_id"},
	ToolSubagentApprove:         {"allow", "id", "patched_args", "request_id", "session_id"},
	ToolSubagentWait:            {"after_seq", "id", "ids", "session_id", "session_ids", "timeout_ms"},
	ToolSubagentEvents:          {"after_seq", "id", "limit", "session_id", "view", "wait_ms"},
	ToolSubagentClose:           {"batch_id", "id", "session_id"},
	ToolSubagentResume:          {"id", "session_id"},
	ToolSubagentApplyWorktree:   {"force", "id", "keep", "paths", "session_id"},
	ToolSubagentDiscardWorktree: {"id", "session_id"},
	ToolTeamSpawn: {
		"allow_existing", "auto_start", "lead_session_id", "max_teammates", "max_writers",
		"status", "strategy", "tasks", "team_id", "teammates", "workspace_id",
	},
	ToolTeamWait:     {"after_seq", "limit", "require_summary", "team_id", "timeout_ms"},
	ToolTeamSend:     {"body", "kind", "metadata", "task_id", "team_id", "to_agent"},
	ToolTeamMailbox:  {"agent_id", "limit", "mark_read", "team_id"},
	ToolTeamTaskSpec: {"task_id", "team_id"},
	ToolTeamTaskContext: {
		"context_budget", "include_dependencies", "include_mailbox",
		"mailbox_limit", "mark_read", "task_id", "team_id",
	},
	ToolTeamTaskReport: {
		"auto_replan", "blocker", "handoff_to", "notify_lead",
		"result_ref", "summary", "task_id", "task_status", "team_id",
	},
	ToolTeamTaskBlock: {
		"auto_replan", "blocker", "handoff_to", "notify_lead",
		"summary", "task_id", "task_status", "team_id",
	},
	ToolSupervisionSnapshot:    {"after_seq", "include_resolved", "limit"},
	ToolSupervisionDescendants: {"after_seq", "health", "include_results", "include_terminal", "limit", "mode"},
	ToolSubagentStatus:         {"after_seq", "health", "include_digest", "include_resolved", "include_results", "include_terminal", "limit", "mode"},
	ToolSubagentInspectTask:    {"agent", "child_session_id", "id", "include_status", "limit", "max_chars", "offset", "sections", "session_id", "target", "task_id"},
	ToolReadAgentResult:        {"id", "limit", "max_chars", "offset", "sections", "task_id"},
	ToolAckLifecycle:           {"notification_id", "decision", "note", "reason", "state", "until", "expected_version"},
	ToolControlDescendant:      {"notification_id", "action", "reason", "cascade", "expected_version", "extend_by_ms", "new_deadline", "extend_which"},
	ToolScheduleCreate:         {"after_seconds", "at", "cron", "daily", "every_seconds", "prompt", "title", "weekly"},
	ToolScheduleList:           {},
	ToolScheduleUpdate:         {"at", "cron", "daily", "every_seconds", "id", "prompt", "title", "weekly"},
	ToolScheduleDelete:         {"id"},
}

// brokerIgnoredArgHint explains what to use instead of a key the addressed tool
// does not implement. Empty means the generic note is enough.
func brokerIgnoredArgHint(toolName, key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "tools_whitelist", "tool_whitelist", "tools", "whitelist", "allowed_tools":
		return "no broker tool restricts its tool surface this way; subagent_spawn_batch task entries take tools_whitelist"
	case "message", "goal", "task", "prompt", "instruction", "instructions",
		"task_description", "objective", "goal_text", "content", "text", "description":
		return "this tool carries no task prompt; subagent_spawn (message, alias goal/task/prompt), subagent_send, subagent_followup and subagent_input do"
	case "read_only", "permission_mode", "difficulty", "difficulty_rationale",
		"task_type", "task_subject", "isolation",
		"fork_context", "fork_turns", "completion_requirement", "provider", "model",
		"reasoning_effort", "thinking_effort":
		return "delegation options are accepted by subagent_spawn, subagent_spawn_batch and team_spawn"
	case "job_id", "job_ref":
		return "job_output addresses a background job by job_id"
	case "task_id", "task_status":
		return "task-scoped tools are team_task_spec, team_task_context, team_task_report, team_task_block and team_send"
	case "team_id", "to_agent":
		return "team-scoped tools are team_spawn, team_wait, team_send, team_mailbox, team_task_spec and team_task_context"
	case "notification_id":
		return "supervision notifications belong to subagent_status(include_digest=true), subagent_ack_lifecycle and subagent_control"
	case "agent_id":
		return "team_mailbox addresses a mailbox by agent_id; child sessions use id/session_id"
	case "target":
		return "subagent_send and subagent_followup address a child by target/id/session_id"
	case "keep":
		return "subagent_apply_worktree is the only tool that accepts keep"
	case "interrupt":
		return "subagent_input is the only tool that accepts interrupt"
	case "allow":
		return "subagent_approve is the only tool that accepts allow"
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
