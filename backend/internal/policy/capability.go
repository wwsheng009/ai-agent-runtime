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

	if caps, ok := controlPlaneToolCapabilities(normalizeToolName(toolName)); ok {
		return caps
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

// controlPlaneToolCapabilities is the single table for runtime control-plane
// tools whose capability needs are not derivable from the generic taxonomy
// heuristics (ReadOnly / Kind / name keywords): a control-plane write is not a
// filesystem write, and Kind alone cannot express agent_management.
//
// Both resolution paths consult this one table:
//   - capabilitiesFromTaxonomy, for the taxonomy-first path a tool with a
//     taxonomy row takes, and
//   - DefaultCapabilityResolver.Resolve, as the fallback for tools without a
//     taxonomy row (or when request metadata supplies one).
//
// Keeping the table in a single place matters because ResolveToolTaxonomy is
// consulted *before* the name lookup: a mapping reachable only from one path
// would silently never run for a tool that has a taxonomy row. That is how
// ack_lifecycle once resolved to read_only alone, making the agent_management
// requirement unreachable dead code.
func controlPlaneToolCapabilities(normalizedToolName string) ([]Capability, bool) {
	switch normalizedToolName {
	case "ask_user_question":
		return []Capability{CapAskUser}, true
	case "enter_plan_mode", "exit_plan_mode":
		// Control tools usable while already in plan mode (read-only + ask_user).
		return []Capability{CapReadOnly, CapAskUser}, true
	case "background_task":
		return []Capability{CapBackgroundTask}, true
	case "task_output":
		return []Capability{CapReadOnly}, true
	case "spawn_agent", "send_message", "followup_task", "send_input", "close_agent", "resume_agent", "resolve_agent_approval", "spawn_team", "send_team_message":
		return []Capability{CapReadOnly, CapAgentManagement}, true
	case "list_agents", "wait_agent", "read_agent_events", "wait_team", "read_mailbox_digest", "read_task_spec", "read_task_context", "report_task_outcome", "block_current_task":
		return []Capability{CapReadOnly}, true
	case "supervision_snapshot", "supervision_descendants":
		// Observation-only supervision reads: no durable write, so read_only is
		// enough and a read-only session may still inspect its own scope.
		return []Capability{CapReadOnly}, true
	case "ack_lifecycle", "control_descendant":
		// Writes to the durable supervision control plane: audit + CAS still
		// apply, and they are never satisfied by a read-only session.
		return []Capability{CapReadOnly, CapAgentManagement}, true
	default:
		return nil, false
	}
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
	case "supervisionsnapshot":
		return "supervision_snapshot"
	case "supervisiondescendants":
		return "supervision_descendants"
	case "acklifecycle":
		return "ack_lifecycle"
	case "controldescendant":
		return "control_descendant"
	default:
		return name
	}
}
