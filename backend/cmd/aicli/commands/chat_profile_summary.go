package commands

import (
	"fmt"
	"strings"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chatProfileSummaryRow 是启动摘要中的一行 profile 生效面描述。
type chatProfileSummaryRow struct {
	label string
	value string
}

// chatProfileSurfaceRows 汇总 profile 的生效面，供 chat 启动摘要展示
// （实施方案 Batch 3 任务 1 / FR-5）：工具与 skills/mcp 的选择计数，以及组合后
// 提示词的体量。token 估算只走 internal/profile 的唯一实现点（estimate.go），
// 展示层不自行换算；结果带"估算"标注。
func chatProfileSurfaceRows(session *ChatSession) []chatProfileSummaryRow {
	if session == nil {
		return nil
	}
	var rows []chatProfileSummaryRow
	if policy := session.ToolPolicy; policy != nil {
		value := fmt.Sprintf("allowlist %d / denylist %d", len(policy.AllowedToolNames()), countEnabledToolFlags(policy.DeniedTools))
		if policy.ReadOnly {
			value += " · read_only"
		}
		rows = append(rows, chatProfileSummaryRow{label: "Profile Tools:", value: value})
	}
	skills := session.ProfileSkillSelection
	if len(skills.Allowlist) > 0 || len(skills.Denylist) > 0 {
		rows = append(rows, chatProfileSummaryRow{
			label: "Profile Skills:",
			value: fmt.Sprintf("allow %d / deny %d", len(skills.Allowlist), len(skills.Denylist)),
		})
	}
	mcpSelection := session.ProfileMCPSelection
	if len(mcpSelection.UseServers) > 0 || len(mcpSelection.ExcludeServers) > 0 {
		rows = append(rows, chatProfileSummaryRow{
			label: "Profile MCP:",
			value: fmt.Sprintf("use %d / exclude %d", len(mcpSelection.UseServers), len(mcpSelection.ExcludeServers)),
		})
	}
	if text := strings.TrimSpace(session.SystemPromptText); text != "" {
		mode := strings.TrimSpace(session.ProfilePromptMode)
		if mode == "" {
			mode = "replace"
		}
		rows = append(rows, chatProfileSummaryRow{
			label: "Profile Prompt:",
			value: fmt.Sprintf("%s · %d B ≈ %d tokens（估算）", mode, len(text), profilesys.EstimateTokensFromBytes(len(text))),
		})
	}
	return rows
}

// countEnabledToolFlags 统计值为 true 的工具名条目；与 AllowedToolNames 的过滤
// 口径保持一致（值非 true 的条目不算生效）。
func countEnabledToolFlags(flags map[string]bool) int {
	count := 0
	for name, enabled := range flags {
		if enabled && strings.TrimSpace(name) != "" {
			count++
		}
	}
	return count
}
