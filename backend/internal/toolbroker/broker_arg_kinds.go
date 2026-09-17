package toolbroker

import "fmt"

// brokerToolArgKinds declares the JSON kind the broker requires for every
// argument that execute() reads with a bare type assertion.
//
// The assertion form `if value, ok := args[key].(bool); ok` is the failure
// class this table closes: a mismatched kind is *not* reported anywhere, the
// request keeps its zero value, and the documented default is silently
// substituted for what the caller asked for. For these keys the default is the
// unsafe direction, so a wrong kind used to change the meaning of a call
// instead of failing it:
//
//   - apply_agent_worktree keep: "true" removed the worktree the caller asked
//     to preserve (keep defaults to false, which deletes it).
//   - resolve_agent_approval allow: "true" was denied as if allow were missing.
//   - read_mailbox_digest/read_task_context mark_read: "true" left the digest
//     unread, so the same messages were reported again on the next call.
//   - list_agents include_closed: "true" hid the closed children the caller
//     asked for.
//   - supervision_snapshot include_resolved: "true" dropped resolved rows.
//   - report_task_outcome/block_current_task notify_lead/auto_replan: "true"
//     silently skipped the lead notification or the replan.
//   - spawn_team allow_existing/auto_start, wait_team require_summary: "false"
//     fell back to the pointer default instead of the requested value.
//   - the identifier keys: a numeric id was dropped, and wait_agent /
//     read_agent_events then silently switched to mailbox-only mode on the
//     caller's own session instead of addressing the child.
//
// Numeric keys are validated here as well as at their read site: this table
// rejects a wrong kind before dispatch (so the error never depends on which
// service the tool reaches first), while the read site keeps the stricter
// coercion rules - a whole-number check, an int narrowing and range guards.
//
// Keys whose kind is already validated by a dedicated tool validator
// (spawn_agent via spawnAgentFieldKinds, send_input via
// validateAgentMessageToolArgTypes, spawn_team task/teammate entries via
// validateSpawnTeamTaskTypes) are not repeated here.
//
// Object- and list-valued keys are covered too, because dropping them is just
// as silent: `startup_acceptance` as a string launched the job with no
// acceptance gate, `patched_args` as a string approved the original arguments,
// and `ids: [1, 2]` emptied the target list so wait_agent fell back to reading
// the caller's own mailbox.
var brokerToolArgKinds = map[string]map[string]string{
	ToolAskUserQuestion: {
		"required":    toolArgFieldBool,
		"suggestions": toolArgFieldStringOrList,
	},
	ToolEnterPlanMode: {
		"plan_path": toolArgFieldString,
	},
	ToolExitPlanMode: {
		"decision": toolArgFieldString,
	},
	ToolBackgroundTask: {
		"command":            toolArgFieldString,
		"cwd":                toolArgFieldString,
		"restart_policy":     toolArgFieldString,
		"startup_acceptance": toolArgFieldObject,
		"timeout_sec":        toolArgFieldNumber,
		"priority":           toolArgFieldNumber,
	},
	ToolTaskOutput: {
		"job_id": toolArgFieldString,
		"offset": toolArgFieldNumber,
		"limit":  toolArgFieldNumber,
	},
	ToolListAgents: {
		"include_closed":    toolArgFieldBool,
		"parent_session_id": toolArgFieldString,
		"path_prefix":       toolArgFieldString,
	},
	ToolSendMessage: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
		"target":     toolArgFieldString,
	},
	ToolFollowupTask: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
		"target":     toolArgFieldString,
	},
	ToolResolveAgentApproval: {
		"allow":        toolArgFieldBool,
		"request_id":   toolArgFieldString,
		"patched_args": toolArgFieldObject,
	},
	ToolWaitAgent: {
		"id":          toolArgFieldString,
		"session_id":  toolArgFieldString,
		"ids":         toolArgFieldStringOrList,
		"session_ids": toolArgFieldStringOrList,
		"after_seq":   toolArgFieldNumber,
		"timeout_ms":  toolArgFieldNumber,
	},
	ToolReadAgentEvents: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
		"view":       toolArgFieldString,
		"after_seq":  toolArgFieldNumber,
		"limit":      toolArgFieldNumber,
		"wait_ms":    toolArgFieldNumber,
	},
	ToolCloseAgent: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
	},
	ToolResumeAgent: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
	},
	ToolApplyAgentWorktree: {
		"keep":       toolArgFieldBool,
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
		"paths":      toolArgFieldStringOrList,
	},
	ToolDiscardAgentWorktree: {
		"id":         toolArgFieldString,
		"session_id": toolArgFieldString,
	},
	// spawn_team is intentionally absent: validateSpawnTeamArgTypes already
	// checks every documented top-level key (including allow_existing,
	// auto_start, team_id, workspace_id, lead_session_id, strategy, status),
	// and the tasks[]/teammates[] entries have their own validators.
	ToolWaitTeam: {
		"require_summary": toolArgFieldBool,
		"team_id":         toolArgFieldString,
		"after_seq":       toolArgFieldNumber,
		"limit":           toolArgFieldNumber,
		"timeout_ms":      toolArgFieldNumber,
	},
	ToolSendTeamMessage: {
		"body":     toolArgFieldString,
		"kind":     toolArgFieldString,
		"metadata": toolArgFieldObject,
		"task_id":  toolArgFieldString,
		"team_id":  toolArgFieldString,
		"to_agent": toolArgFieldString,
	},
	ToolReadMailboxDigest: {
		"agent_id":  toolArgFieldString,
		"mark_read": toolArgFieldBool,
		"team_id":   toolArgFieldString,
		"limit":     toolArgFieldNumber,
	},
	ToolReadTaskSpec: {
		"task_id": toolArgFieldString,
		"team_id": toolArgFieldString,
	},
	ToolReadTaskContext: {
		"include_dependencies": toolArgFieldBool,
		"include_mailbox":      toolArgFieldBool,
		"mark_read":            toolArgFieldBool,
		"task_id":              toolArgFieldString,
		"team_id":              toolArgFieldString,
		"context_budget":       toolArgFieldNumber,
		"mailbox_limit":        toolArgFieldNumber,
	},
	ToolReportTaskOutcome: {
		"auto_replan": toolArgFieldBool,
		"blocker":     toolArgFieldString,
		"handoff_to":  toolArgFieldString,
		"notify_lead": toolArgFieldBool,
		"result_ref":  toolArgFieldString,
		"summary":     toolArgFieldString,
		"task_id":     toolArgFieldString,
		"task_status": toolArgFieldString,
		"team_id":     toolArgFieldString,
	},
	ToolBlockCurrentTask: {
		"auto_replan": toolArgFieldBool,
		"blocker":     toolArgFieldString,
		"handoff_to":  toolArgFieldString,
		"notify_lead": toolArgFieldBool,
		"summary":     toolArgFieldString,
		"task_id":     toolArgFieldString,
		"task_status": toolArgFieldString,
		"team_id":     toolArgFieldString,
	},
	ToolSupervisionSnapshot: {
		"include_resolved": toolArgFieldBool,
		"after_seq":        toolArgFieldNumber,
		"limit":            toolArgFieldNumber,
	},
	ToolSupervisionDescendants: {
		"include_terminal": toolArgFieldBool,
		"include_results":  toolArgFieldBool,
		"health":           toolArgFieldString,
		"mode":             toolArgFieldString,
		"after_seq":        toolArgFieldNumber,
		"limit":            toolArgFieldNumber,
	},
	ToolReadAgentResult: {
		"id":        toolArgFieldString,
		"task_id":   toolArgFieldString,
		"sections":  toolArgFieldStringOrList,
		"max_chars": toolArgFieldNumber,
	},
	ToolAckLifecycle: {
		"decision":         toolArgFieldString,
		"note":             toolArgFieldString,
		"notification_id":  toolArgFieldString,
		"reason":           toolArgFieldString,
		"state":            toolArgFieldString,
		"until":            toolArgFieldString,
		"expected_version": toolArgFieldNumber,
	},
	ToolControlDescendant: {
		"action":           toolArgFieldString,
		"cascade":          toolArgFieldString,
		"notification_id":  toolArgFieldString,
		"reason":           toolArgFieldString,
		"expected_version": toolArgFieldNumber,
	},
}

// validateBrokerToolArgKinds rejects an argument whose JSON kind does not match
// the kind the tool reads. It runs before dispatch, so the caller receives the
// mismatch instead of a silently defaulted call. Unknown keys stay advisory and
// are reported by annotateIgnoredBrokerToolArgs, because models legitimately add
// harmless extra keys that must not fail a call.
func validateBrokerToolArgKinds(toolName string, args map[string]interface{}) error {
	if len(args) == 0 {
		return nil
	}
	name := normalizeToolName(toolName)
	kinds, ok := brokerToolArgKinds[name]
	if !ok {
		return nil
	}
	for key, want := range kinds {
		value, exists := args[key]
		if !exists || value == nil {
			continue
		}
		if brokerToolArgMatchesKind(value, want) {
			continue
		}
		message := fmt.Sprintf("%s argument %q must be a %s, got %T (%v)",
			name, key, want, value, value)
		if hint := brokerArgKindHint(key); hint != "" {
			message += "; " + hint
		}
		return fmt.Errorf("%s", message)
	}
	return validateBrokerToolNestedArgKinds(name, args)
}

func brokerToolArgMatchesKind(value interface{}, want string) bool {
	switch want {
	case toolArgFieldString:
		_, ok := value.(string)
		return ok
	case toolArgFieldBool:
		_, ok := value.(bool)
		return ok
	case toolArgFieldNumber:
		return isToolJSONNumber(value)
	case toolArgFieldObject:
		_, ok := value.(map[string]interface{})
		return ok
	case toolArgFieldStringOrList:
		switch typed := value.(type) {
		case string:
			return true
		case []string:
			return true
		case []interface{}:
			for _, item := range typed {
				if _, ok := item.(string); !ok {
					return false
				}
			}
			return true
		default:
			return false
		}
	default:
		return true
	}
}

// startupAcceptanceFieldKinds are the nested startup_acceptance keys read with a
// bare string assertion. The numeric keys are covered by the strict readers at
// the read site; the string keys are validated here so a wrong kind cannot
// quietly launch a job whose acceptance probe never runs.
var startupAcceptanceFieldKinds = map[string]string{
	"probe":   toolArgFieldString,
	"address": toolArgFieldString,
	"url":     toolArgFieldString,
}

func validateBrokerToolNestedArgKinds(name string, args map[string]interface{}) error {
	if name != ToolBackgroundTask {
		return nil
	}
	startup, ok := args["startup_acceptance"].(map[string]interface{})
	if !ok {
		return nil
	}
	for key, want := range startupAcceptanceFieldKinds {
		value, exists := startup[key]
		if !exists || value == nil {
			continue
		}
		if brokerToolArgMatchesKind(value, want) {
			continue
		}
		return fmt.Errorf("%s startup_acceptance argument %q must be a %s, got %T (%v); omit it to skip the startup probe",
			name, key, want, value, value)
	}
	return nil
}

// brokerArgKindHint names the safe direction for a rejected key, so the model
// retries with a usable value instead of dropping the argument entirely.
func brokerArgKindHint(key string) string {
	switch key {
	case "keep":
		return "pass true to keep the worktree after applying it, or omit it to remove the worktree"
	case "allow":
		return "pass true to approve or false to deny; a non-boolean value leaves the pending approval undecided"
	case "mark_read":
		return "pass true to mark the reported messages read, or omit it to keep them unread"
	case "include_closed", "include_resolved", "include_dependencies", "include_mailbox":
		return "pass true to include the extra rows in the result, or omit it to keep the compact default"
	case "notify_lead", "auto_replan":
		return "pass true to notify the lead or trigger the replan, or omit it to skip it"
	case "allow_existing", "auto_start":
		return "pass the boolean explicitly; omitting it uses the runtime default"
	case "required":
		return "pass false to allow an unanswered question, or omit it for the required default"
	case "startup_acceptance":
		return "pass a JSON object such as {\"probe\":\"process\"}, not a stringified object"
	case "patched_args":
		return "pass the replacement arguments as a JSON object, not a stringified object"
	case "metadata":
		return "pass the message metadata as a JSON object"
	case "ids", "session_ids":
		return "pass one session reference as a string, or a JSON array of strings"
	case "paths", "suggestions":
		return "pass a JSON array of strings, or a single string"
	case "id", "session_id", "target", "team_id", "agent_id", "task_id", "job_id", "request_id", "notification_id", "to_agent":
		return "handle references are strings as returned by the runtime; a non-string reference can never be resolved"
	default:
		return ""
	}
}
