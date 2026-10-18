package prompt

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// ToolGuidanceFacts describes which tools are actually visible to the model so
// tool-specific guidance sections can be withheld when their subject tools are
// absent (profile denylist / read-only stripping / disabled components).
//
// A nil *ToolGuidanceFacts means "facts unavailable": every render helper keeps
// the legacy unconditional output, byte-for-byte. This is the compatibility
// contract for hosts that have not wired a policy-derived facts value yet.
type ToolGuidanceFacts struct {
	// Only lists model-visible tool names (lowercase) when an allowlist
	// governs the surface; nil = no allowlist (everything present).
	Only map[string]bool
	// Denied lists names removed from the surface (lowercase); wins over Only.
	Denied map[string]bool
}

// any reports whether at least one candidate tool name is model-visible.
func (f *ToolGuidanceFacts) any(names ...string) bool {
	if f == nil {
		return true
	}
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if f.Denied[key] {
			continue
		}
		if f.Only == nil || f.Only[key] {
			return true
		}
	}
	return false
}

// WriteLikeTools lists the tools read-only policies strip from the model
// surface (mirrors agentdef builtin DisallowedTools / policy denial text).
var WriteLikeTools = []string{"write", "edit", "multiedit", "append_write", "apply_patch"}

var (
	// 下列名单只写规范名；CallableNames 展开现役别名（bash/rg/read/...），
	// 使可见性检测对模型可能请求的任意拼写都成立，而拼写表只在
	// internal/toolregistry 维护一份。
	shellGuidanceTools       = toolregistry.CallableNames("shell", "aicli_exec", "grep", "view", "ls", "glob")
	fileEditingGuidanceTools = toolregistry.CallableNames("apply_patch", "edit", "write", "append_write", "multiedit")
	parallelGuidanceTools    = toolregistry.CallableNames("view", "grep", "glob", "ls", "shell", "aicli_exec")
	delegationGuidanceTools  = toolregistry.CallableNames("subagent_spawn_batch", "subagent_spawn")
	multiAgentGuidanceTools  = toolregistry.CallableNames("subagent_spawn_batch", "subagent_spawn", "team_spawn", "subagent_wait", "team_wait")
	todosGuidanceTools       = toolregistry.CallableNames("todos")
)

// RenderShellExecutionGuidanceFacts renders the shell/toolkit guidance only
// when at least one subject tool is model-visible. Empty guidance keeps hosts
// from steering the model toward tools it cannot call.
func RenderShellExecutionGuidanceFacts(facts *ToolGuidanceFacts, capabilityGuidance string) string {
	if !facts.any(shellGuidanceTools...) {
		return ""
	}
	return RenderShellExecutionGuidanceWithCapability(capabilityGuidance)
}

// RenderFileEditingGuidanceFacts renders file-editing guidance only when a
// write-capable file tool is visible (read-only surfaces skip the section).
func RenderFileEditingGuidanceFacts(facts *ToolGuidanceFacts) string {
	if !facts.any(fileEditingGuidanceTools...) {
		return ""
	}
	return RenderFileEditingGuidance()
}

// RenderTodosGuidanceFacts renders the task-list rule only when `todos` is
// model-visible. A child whose surface omits todos must not be told to use it.
func RenderTodosGuidanceFacts(facts *ToolGuidanceFacts) string {
	if !facts.any(todosGuidanceTools...) {
		return ""
	}
	return RenderTodosGuidance()
}

// RenderParallelToolGuidanceFacts renders batching guidance only when the
// batchable read tools / shell surface is visible.
func RenderParallelToolGuidanceFacts(facts *ToolGuidanceFacts) string {
	if !facts.any(parallelGuidanceTools...) {
		return ""
	}
	return RenderParallelToolGuidance()
}

// RenderTaskDifficultyGuidanceFacts renders the delegation policy only when a
// spawn tool is visible; a model without spawn tools cannot act on it.
func RenderTaskDifficultyGuidanceFacts(facts *ToolGuidanceFacts) string {
	if !facts.any(delegationGuidanceTools...) {
		return ""
	}
	return RenderTaskDifficultyGuidance()
}

// RenderMultiAgentCollaborationGuidanceFacts renders the collaboration policy
// only when orchestration tools are visible.
func RenderMultiAgentCollaborationGuidanceFacts(facts *ToolGuidanceFacts) string {
	if !facts.any(multiAgentGuidanceTools...) {
		return ""
	}
	return RenderMultiAgentCollaborationGuidance()
}
