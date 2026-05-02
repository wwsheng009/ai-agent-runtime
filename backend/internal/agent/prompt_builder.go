package agent

import (
	"fmt"
	"strings"

	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

// PromptBuilder 为子代理生成专用 system prompt。
type PromptBuilder struct{}

// NewPromptBuilder 创建默认 PromptBuilder。
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

// BuildSubagentPrompt 生成 fresh child conversation 的系统提示。
func (b *PromptBuilder) BuildSubagentPrompt(parent *Config, task SubagentTask) string {
	lines := make([]string, 0, 10)

	if parent != nil && strings.TrimSpace(parent.SystemPrompt) != "" {
		lines = append(lines, strings.TrimSpace(parent.SystemPrompt))
	}

	lines = append(lines, "You are an isolated subagent working for a parent coding agent.")
	if task.Role != "" {
		lines = append(lines, "Subagent role: "+task.Role)
	}
	lines = append(lines, "Focus only on your assigned subtask and return a concise final report.")
	lines = append(lines, "The parent receives only your compressed report, not your full transcript.")
	lines = append(lines, "Do not change the overall plan unless the subtask requires it.")
	lines = append(lines, "When writing or editing files, prefer small patches and chunked file-tool calls over one huge inline payload.")
	lines = append(lines, "For long file generation, prefer skeleton first, then append_write chunks, then apply_patch/edit cleanup.")
	if guidance := strings.TrimSpace(runtimeprompt.RenderParallelToolGuidance()); guidance != "" {
		lines = append(lines, guidance)
	}

	if task.ReadOnly {
		lines = append(lines, "This is a read-only subagent. Never perform or propose direct workspace mutations.")
		lines = append(lines, "Prefer findings, evidence, failures, and open questions.")
	} else {
		lines = append(lines, "This subagent may act as the single writer only if the scheduler permits it.")
	}

	if len(task.ToolsWhitelist) > 0 {
		lines = append(lines, fmt.Sprintf("Allowed tools: %s.", strings.Join(task.ToolsWhitelist, ", ")))
	}
	if len(task.DependsOn) > 0 {
		lines = append(lines, fmt.Sprintf("Depends on completed subagents: %s.", strings.Join(task.DependsOn, ", ")))
	}
	if len(task.PatchContext) > 0 {
		lines = append(lines, "Patch context:")
		for _, patch := range task.PatchContext {
			patchLine := "- "
			if patch.Path != "" {
				patchLine += patch.Path
			} else {
				patchLine += "patch"
			}
			if patch.Summary != "" {
				patchLine += " - " + patch.Summary
			}
			if patch.ApplyStatus != "" {
				patchLine += " [apply=" + patch.ApplyStatus + "]"
			}
			if patch.VerificationStatus != "" {
				patchLine += " [verify=" + patch.VerificationStatus + "]"
			}
			lines = append(lines, patchLine)
			if len(patch.ArtifactRefs) > 0 {
				lines = append(lines, "Artifact refs: "+strings.Join(patch.ArtifactRefs, ", "))
			}
			if preview := previewPatchDiff(patch.Diff); preview != "" {
				lines = append(lines, preview)
			}
		}
	}
	if task.BudgetTokens > 0 {
		lines = append(lines, fmt.Sprintf("Token budget: %d.", task.BudgetTokens))
	}
	if task.TimeoutSec > 0 {
		lines = append(lines, fmt.Sprintf("Time budget: %d seconds.", task.TimeoutSec))
	}

	lines = append(lines, "Assigned goal: "+strings.TrimSpace(task.Goal))
	return strings.Join(lines, "\n")
}

func previewPatchDiff(diff string) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}
	lines := strings.Split(diff, "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	preview := strings.Join(lines, "\n")
	if len(preview) > 400 {
		preview = preview[:397] + "..."
	}
	return "Patch diff excerpt:\n" + preview
}
