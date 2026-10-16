package policy

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
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

// AllUserDisablableCapabilities lists the capability domains a profile may
// disable via tools.disabled_capabilities. read_only and exec_shell are
// excluded on purpose: disabling them removes nearly every tool (read_only is
// the floor every tool resolves to; exec_shell is the shell family itself) and
// the existing read_only flag / allowlist express those intents precisely.
func AllUserDisablableCapabilities() []Capability {
	return []Capability{
		CapWriteFS,
		CapNetwork,
		CapExternalSideEffect,
		CapAskUser,
		CapBackgroundTask,
		CapAgentManagement,
	}
}

// CapabilitiesComplementOf returns the disablable universe minus disabled.
// Unknown names in disabled are ignored by the caller (validation reports
// them); the result preserves the universe order for determinism.
func CapabilitiesComplementOf(disabled []Capability) []Capability {
	blocked := make(map[Capability]bool, len(disabled))
	for _, capability := range disabled {
		blocked[capability] = true
	}
	complement := make([]Capability, 0, 8)
	for _, capability := range AllUserDisablableCapabilities() {
		if !blocked[capability] {
			complement = append(complement, capability)
		}
	}
	// The floor domains always stay allowed so the scope never amputates the
	// tool surface wholesale.
	return append(complement, CapReadOnly, CapExecShell)
}

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
	// shell 判定与 IsWriteLikeToolName 同源：注册表事实优先，未登记名字回退
	// 子串启发式（含 TrimSpace/小写归一）。此前这里是手写的三段 Contains，
	// 漏掉了 TrimSpace 与小写，也就成了真源之外的第二份启发式。
	if IsShellLikeToolName(toolName) {
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
	case "plan_enter", "plan_exit":
		// Control tools usable while already in plan mode (read-only + ask_user).
		return []Capability{CapReadOnly, CapAskUser}, true
	case "job_start":
		return []Capability{CapBackgroundTask}, true
	case "job_cancel", "job_control":
		// 终止后台进程与启动它同级：都要 background_task 能力，并在
		// accept_edits/dont_ask 等模式下走 ask/deny。
		return []Capability{CapBackgroundTask}, true
	case "job_input":
		// Driving a running session's stdin is the same authority as starting
		// it: a capability scope that allows background_task also allows
		// job_input, and a read-only scope blocks it (2026-10-11).
		return []Capability{CapBackgroundTask}, true
	case "job_monitor":
		// 巡检本身只读，但 max_duration_ms 能终止进程，故与 background_task
		// 同级授权（宁可多问一次，不给只读通道开后门）。
		return []Capability{CapBackgroundTask}, true
	case "job_output", "job_list", "job_search":
		return []Capability{CapReadOnly}, true
	case "spawn_subagents":
		// agent 侧注册的批量委派入口（agent.SpawnSubagentsToolName），与
		// subagent_spawn / team_spawn 是同一授权族，必须同级登记。它此前既没有
		// taxonomy 行、也不在本表，解析落到名字启发式只得到 read_only ⇒
		// `disabled_capabilities: [agent_management]` 关得住两个兄弟入口，却关不住
		// 它。这正是本函数头注释里 ack_lifecycle 那类"表外漏项 = 门控失效"。
		return []Capability{CapReadOnly, CapAgentManagement}, true
	case "subagent_spawn", "subagent_send", "subagent_followup", "subagent_input", "subagent_close", "subagent_resume", "subagent_approve", "team_spawn", "team_send":
		return []Capability{CapReadOnly, CapAgentManagement}, true
	case "subagent_list", "subagent_wait", "subagent_events", "team_wait", "team_mailbox", "team_task_spec", "team_task_context", "team_task_report", "team_task_block":
		return []Capability{CapReadOnly}, true
	case "supervision_snapshot", "supervision_descendants", "read_agent_result", "subagent_status", "subagent_inspect_task":
		// Observation-only supervision reads: no durable write, so read_only is
		// enough and a read-only session may still inspect its own scope.
		return []Capability{CapReadOnly}, true
	case "subagent_ack_lifecycle", "subagent_control":
		// Writes to the durable supervision control plane: audit + CAS still
		// apply, and they are never satisfied by a read-only session.
		return []Capability{CapReadOnly, CapAgentManagement}, true
	case "subagent_apply_worktree", "subagent_discard_worktree":
		// Landing (or dropping) an isolated child's worktree mutates the main
		// repository and the child's lifecycle: a control-plane write, not a
		// plain filesystem write. A capability-scoped policy must grant both
		// write_fs and agent_management explicitly, so neither a write-only nor
		// a read-only (even one that holds agent_management) scope can land or
		// destroy a worktree by inheriting the requirement from the tool name.
		return []Capability{CapWriteFS, CapAgentManagement}, true
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

// normalizeToolName 把任意工具名拼写归一到规范名。
//
// 归一表（含 camelCase / 旧拼写 / 退役名替代）的唯一真源是
// internal/toolregistry.Normalize；本函数只做转发，供包内既有调用点使用。
// 保留包内名字是为了不让 30+ 处调用点在本轮收敛里一起变形，从而让
// "policy 是否仍与真源一致"这件事保持可逐字节核对。
func normalizeToolName(name string) string {
	return toolregistry.Normalize(name)
}

// canonicalToolName 归一到**规范名**：在拼写归一之上再折叠别名与退役名
// （read→view、rg→grep、bash/execute_shell_command→shell、spawn_agent→subagent_spawn）。
//
// 与 normalizeToolName 的分工：需要回答"这个名字属于哪个工具"时用本函数；
// 只做拼写规范化（例如 taxonomy 行自洽性检查）时用 normalizeToolName。
// 两者都转发到 internal/toolregistry，policy 不再自持任何名字表。
func canonicalToolName(name string) string {
	return toolregistry.Canonical(name)
}
