package agent

import (
	"fmt"
	"regexp"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 模型可见的拒绝错误码（稳定文本标记，随 tool_result 进入模型上下文）。
// 机器可读事件字段仍使用 errors.ErrAgentReadOnly；这里只负责"模型读得懂"的
// 细分原因，帮助模型按类别选择出路，而不是靠猜。
const (
	DenialCodeReadOnlyTool          = "ERR_READONLY_TOOL"
	DenialCodeReadOnlyShell         = "ERR_READONLY_SHELL"
	DenialCodeReadOnlyShellCompound = "ERR_READONLY_SHELL_COMPOUND"
	DenialCodeReadOnlyShellDynamic  = "ERR_READONLY_SHELL_DYNAMIC"
)

// 只读拒绝熔断阈值（M6）。连续拒绝达到 Escalate 阈值后，向父会话邮箱发送
// subagent.requires_write 事件并给模型强提示；达到 HardStop 阈值后终止本轮，
// 终态消息明确"goal requires write access; re-spawn with read_only=false"，
// 不再以 step=unlimited 无限空转。
const (
	ReadOnlyDenyEscalateThreshold = 3
	ReadOnlyDenyHardStopThreshold = 5
)

// renderDenialGuidance 把 read_only 策略的裸错误串升级为"原因+规则+修复"的
// 模型可读文本；非只读拒绝原样返回（向后兼容，不改变其他策略的语义）。
// 注意：文本中保留原始 reason 行（含 "read-only"），使下游
// classifyDeniedPolicy / enrichDeniedToolMetadata 的字符串分类仍然命中。
func renderDenialGuidance(toolName, reason string) string {
	if classifyDeniedPolicy(reason) != "read_only" {
		return reason
	}
	code := denialCodeForReason(reason)
	return fmt.Sprintf(
		"[TOOL_DENIED:%s] %s\nboundary: read_only (hard child boundary; approval and bypass_permissions cannot widen it)\nrule: %s\nfix: %s",
		code, toolName, reason, denialFixForCode(code))
}

// denialCodeForReason 依据策略错误串的稳定片段细分错误码。片段来自
// internal/policy/tool_policy.go 的 AllowTool / AllowToolCallWithContext 文案，
// 若上游文案调整，此处按语义（compound/dynamic/write-like/shell）继续命中。
func denialCodeForReason(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "compound"):
		return DenialCodeReadOnlyShellCompound
	case strings.Contains(lower, "redirection") || strings.Contains(lower, "dynamic command"):
		return DenialCodeReadOnlyShellDynamic
	case strings.Contains(lower, "write-like tool") || strings.Contains(lower, "background command"):
		return DenialCodeReadOnlyTool
	default:
		return DenialCodeReadOnlyShell
	}
}

// denialFixForCode 给出与错误码对应的出路。写型工具被拒时直接引用共享出路文案，
// 与子代理 prompt 横幅、父代理 spawn 报告保持同源。
func denialFixForCode(code string) string {
	switch code {
	case DenialCodeReadOnlyTool:
		return "Stop attempting this tool. " + runtimepolicy.ReadOnlyEscalationPathText
	case DenialCodeReadOnlyShellCompound:
		return "Split the command: submit exactly one read-only command per shell.commands entry (chained/compound commands cannot be validated independently)."
	case DenialCodeReadOnlyShellDynamic:
		return "The read-only boundary rejects redirection, variable expansion, and command substitution; submit plain single commands only."
	default:
		return "The command is not on the read-only allow list. Use git status/diff/log/show, rg, ls/glob, Get-Content, pwd, echo, or " + runtimepolicy.ReadOnlyEscalationPathText
	}
}

// ordinalSuffix 把 n 渲染成英文序数词（1st/2nd/3rd/4th…），用于强度递增的
// 熔断提示文案。
func ordinalSuffix(n int) string {
	rem100 := n % 100
	if rem100 >= 11 && rem100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

// readOnlyDenyEscalationAdvisory 在连续拒绝达到 Escalate 阈值时追加到模型可见
// 文本与 subagent.requires_write 事件中，明确要求停止尝试并给出终止选项。
func readOnlyDenyEscalationAdvisory(streak int) string {
	return fmt.Sprintf(
		"This is the %d%s consecutive read-only denial in this run. STOP attempting write operations: this agent cannot mutate the workspace. Conclude with a report to the parent listing the exact change you needed, and ask the parent to apply it or re-spawn you with read_only=false.",
		streak, ordinalSuffix(streak))
}

// readOnlyDenialLimitReachedMessage 是硬停止阈值触发时的终态消息。
func readOnlyDenialLimitReachedMessage(streak, threshold int) string {
	return fmt.Sprintf(
		"Run stopped after %d consecutive read-only denials (threshold %d): the goal requires write access that this read-only child cannot obtain. Re-spawn with read_only=false or accept the read-only report.",
		streak, threshold)
}

var writeIntentPattern = regexp.MustCompile(`(?i)\b(write|writes?|editing?|create|creates?|modify|implement|apply[\s_-]?patch|append|delete|save|commit|install|scaffold|refactor|patch)\b`)

var writeIntentChineseSignals = []string{
	"写文件", "写入", "创建", "修改", "生成文件", "保存为", "删除文件", "实现功能", "打补丁", "写代码", "改动",
}

// goalHasWriteIntent 对子代理 goal 做启发式写意图检测（M5）。只用于非阻断的
// route_warnings 提示，允许少量误报；命中并不改变 spawn 行为。
func goalHasWriteIntent(goal string) bool {
	if writeIntentPattern.MatchString(goal) {
		return true
	}
	lower := strings.ToLower(goal)
	for _, signal := range writeIntentChineseSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// writeIntentRouteWarning 是 M5 追加到 route_warnings 的固定文案，父模型在
// spawn 工具结果里即时可见，可在决策点纠正 read_only 与 goal 的不匹配。
func writeIntentRouteWarning() string {
	return "goal_appears_to_require_writes: read_only=true will strip all write-like tools from this child and deny them at execution; set read_only=false for writer tasks, or narrow the goal to read-only analysis and report findings back to the parent"
}

// enrichReadOnlyToolDescriptions 在只读子代理的模型可见工具面上，给 shell 工具
// description 动态追加 READ-ONLY MODE 预告，让模型第一轮就知道 shell 边界，
// 而不是逐个命令试错后被拒。非只读策略原样返回。
func enrichReadOnlyToolDescriptions(tools []types.ToolDefinition, policy *runtimepolicy.ToolExecutionPolicy) []types.ToolDefinition {
	if policy == nil || !policy.ReadOnly {
		return tools
	}
	const readOnlyShellModeNote = "\n\nREAD-ONLY MODE: only read-only commands are allowed (git status/diff/log/show, rg, ls/glob, Get-Content, Select-Object, pwd, echo). Write, mutating, redirecting, or compound commands are hard-denied. If you need to change files, return the change to the parent instead."
	changed := false
	out := make([]types.ToolDefinition, 0, len(tools))
	for _, def := range tools {
		if (def.Name == "shell" || def.Name == "bash") && !strings.Contains(def.Description, "READ-ONLY MODE") {
			def.Description += readOnlyShellModeNote
			changed = true
		}
		out = append(out, def)
	}
	if !changed {
		return tools
	}
	return out
}
