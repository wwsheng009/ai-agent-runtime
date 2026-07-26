package policy

import (
	"strings"
)

// Capability describes an abstract resource capability required by a tool.
type Capability string

const (
	CapReadOnly           Capability = "read_only"
	CapWriteFS            Capability = "write_fs"
	CapExecShell          Capability = "exec_shell"
	CapNetwork            Capability = "network"
	CapExternalSideEffect Capability = "external_side_effect"
	CapAskUser            Capability = "ask_user"
	CapBackgroundTask     Capability = "background_task"
	CapAgentManagement    Capability = "agent_management"
)

// CapabilityResolver determines required capabilities for a tool call.
type CapabilityResolver interface {
	Resolve(req EvalRequest) []Capability
}

// DefaultCapabilityResolver applies taxonomy first, then basic heuristics on tool name.
type DefaultCapabilityResolver struct{}

// Resolve returns capabilities for the given request.
func (r DefaultCapabilityResolver) Resolve(req EvalRequest) []Capability {
	toolName := strings.ToLower(strings.TrimSpace(req.ToolName))
	if toolName == "" {
		return []Capability{CapReadOnly}
	}

	meta := req.Metadata
	if meta == nil && req.ToolInfo != nil && len(req.ToolInfo.Metadata) > 0 {
		meta = req.ToolInfo.Metadata
	}
	if tax, ok := ResolveToolTaxonomy(toolName, meta); ok {
		if caps := capabilitiesFromTaxonomy(tax); len(caps) > 0 {
			return caps
		}
	}

	switch normalizeToolName(toolName) {
	case "ask_user_question":
		return []Capability{CapAskUser}
	case "enter_plan_mode", "exit_plan_mode":
		return []Capability{CapReadOnly, CapAskUser}
	case "background_task":
		return []Capability{CapBackgroundTask}
	case "task_output":
		return []Capability{CapReadOnly}
	case "spawn_agent", "send_message", "followup_task", "send_input", "close_agent", "resume_agent", "resolve_agent_approval", "spawn_team", "send_team_message":
		return []Capability{CapReadOnly, CapAgentManagement}
	case "list_agents", "wait_agent", "read_agent_events", "wait_team", "read_mailbox_digest", "read_task_spec", "read_task_context", "report_task_outcome", "block_current_task":
		return []Capability{CapReadOnly}
	}

	caps := make([]Capability, 0, 3)
	if IsWriteLikeToolName(toolName) {
		caps = append(caps, CapWriteFS)
	} else {
		caps = append(caps, CapReadOnly)
	}
	if strings.Contains(toolName, "bash") || strings.Contains(toolName, "shell") || strings.Contains(toolName, "exec") {
		caps = append(caps, CapExecShell)
	}
	if strings.Contains(toolName, "fetch") || strings.Contains(toolName, "http") || strings.Contains(toolName, "web") || strings.Contains(toolName, "download") || strings.Contains(toolName, "sourcegraph") || strings.Contains(toolName, "search") {
		caps = append(caps, CapNetwork)
	}
	if strings.Contains(toolName, "email") || strings.Contains(toolName, "slack") || strings.Contains(toolName, "notify") {
		caps = append(caps, CapExternalSideEffect)
	}
	return dedupeCapabilities(caps)
}

func dedupeCapabilities(values []Capability) []Capability {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[Capability]bool, len(values))
	out := make([]Capability, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuserquestion":
		return "ask_user_question"
	case "enterplanmode":
		return "enter_plan_mode"
	case "exitplanmode":
		return "exit_plan_mode"
	case "backgroundtask":
		return "background_task"
	case "taskoutput":
		return "task_output"
	case "spawnagent":
		return "spawn_agent"
	case "listagents":
		return "list_agents"
	case "sendmessage":
		return "send_message"
	case "followuptask":
		return "followup_task"
	case "sendinput":
		return "send_input"
	case "waitagent":
		return "wait_agent"
	case "readagentevents":
		return "read_agent_events"
	case "closeagent":
		return "close_agent"
	case "resumeagent":
		return "resume_agent"
	case "resolveagentapproval":
		return "resolve_agent_approval"
	case "spawnteam":
		return "spawn_team"
	case "waitteam":
		return "wait_team"
	case "sendteammessage":
		return "send_team_message"
	case "readmailboxdigest":
		return "read_mailbox_digest"
	case "readtaskspec":
		return "read_task_spec"
	case "readtaskcontext":
		return "read_task_context"
	case "reporttaskoutcome":
		return "report_task_outcome"
	case "blockcurrenttask":
		return "block_current_task"
	default:
		return name
	}
}
