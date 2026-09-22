package agent

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

// ---------------------------------------------------------------------------
// G7：路由回执（route receipt）。
//
// 父代理在 spawn 决策点就能看到每个孩子被路由到哪，而不是事后去查事件库：
//   - 同步批次走 renderSubagentResults 的 `route:` 行；
//   - spawn_agent 走工具结果的 cache-safe 摘要（internal/toolbroker）。
//
// 数据源与审计事件同源（RouteDecision / 请求侧提示），因此不会出现
// 「回执说 A、审计说 B」的双口径。回执只做展示，不参与任何路由决策。
// ---------------------------------------------------------------------------

const (
	// maxSubagentRouteReceiptLines 与方案 §5.7 的「最多 8 行」一致。
	maxSubagentRouteReceiptLines = 8
	// maxSubagentRouteReceiptBytes 是回执总字节上限（方案 §5.7 的 1 KB）。
	maxSubagentRouteReceiptBytes = 1024
)

// subagentRouteReceipt 是一个子代理的紧凑路由回执。
type subagentRouteReceipt struct {
	Difficulty       string
	DifficultySource string
	TaskType         string
	TaskSubject      string
	Provider         string
	Model            string
	ReasoningEffort  string
	Warnings         []string
}

// empty 表示这条回执没有任何路由信息（例如路由整体关闭）。调用方据此
// 决定是否整批省略回执行，避免给未启用路由的批次刷出无意义的 unrouted 噪音。
func (r subagentRouteReceipt) empty() bool {
	return strings.TrimSpace(r.Difficulty) == "" &&
		strings.TrimSpace(r.Provider) == "" &&
		strings.TrimSpace(r.Model) == ""
}

// subagentRouteReceiptFromTask 用请求侧提示兜底：校验/构建/闸门在解析前就
// 失败的早退路径没有 RouteDecision，此时回执写明请求的难度与来源，而不是
// 假装「已路由」。
func subagentRouteReceiptFromTask(task SubagentTask) subagentRouteReceipt {
	return subagentRouteReceipt{
		Difficulty:      strings.TrimSpace(task.Difficulty),
		TaskType:        strings.TrimSpace(task.TaskType),
		TaskSubject:     strings.TrimSpace(task.TaskSubject),
		Provider:        strings.TrimSpace(task.Provider),
		Model:           strings.TrimSpace(task.Model),
		ReasoningEffort: strings.TrimSpace(task.ReasoningEffort),
		Warnings:        append([]string(nil), task.RouteWarnings...),
	}
}

// subagentRouteReceiptFromDecision 用解析结果覆盖兜底值：这是权威口径，
// 与 subagent.route.resolved / subagent.completed 审计载荷取自同一 decision。
func subagentRouteReceiptFromDecision(decision modelrouting.RouteDecision) subagentRouteReceipt {
	return subagentRouteReceipt{
		Difficulty:       strings.TrimSpace(decision.Difficulty),
		DifficultySource: strings.TrimSpace(decision.Source),
		TaskType:         strings.TrimSpace(decision.TaskType),
		TaskSubject:      strings.TrimSpace(decision.TaskSubject),
		Provider:         strings.TrimSpace(decision.Provider),
		Model:            strings.TrimSpace(decision.Model),
		ReasoningEffort:  strings.TrimSpace(decision.ReasoningEffort),
		Warnings:         append([]string(nil), decision.Warnings...),
	}
}

// line 渲染单行回执。未路由时明确写 unrouted（§5.7 的边界约束），而不是留空。
func (r subagentRouteReceipt) line(id string) string {
	label := firstNonEmptySubagentValue(strings.TrimSpace(id), "subagent")
	difficulty := strings.TrimSpace(r.Difficulty)
	switch {
	case difficulty == "":
		difficulty = "unrouted"
	case strings.TrimSpace(r.DifficultySource) != "":
		difficulty += "(" + strings.TrimSpace(r.DifficultySource) + ")"
	}
	target := "unrouted"
	if provider, model := strings.TrimSpace(r.Provider), strings.TrimSpace(r.Model); provider != "" || model != "" {
		target = strings.Trim(provider+"/"+model, "/")
	}
	parts := []string{"route: " + label, difficulty, target}
	if taskType := strings.TrimSpace(r.TaskType); taskType != "" {
		parts = append(parts, "task_type="+taskType)
	}
	if subject := strings.TrimSpace(r.TaskSubject); subject != "" {
		// task_subject 是自由文本：截断到 48 字节，避免撑爆 1 KB 回执预算。
		if len(subject) > 48 {
			subject = subject[:48] + "…"
		}
		parts = append(parts, "subject="+subject)
	}
	if effort := strings.TrimSpace(r.ReasoningEffort); effort != "" {
		parts = append(parts, "effort="+effort)
	}
	if count := len(r.Warnings); count > 0 {
		parts = append(parts, fmt.Sprintf("warnings=%d", count))
	}
	return "  " + strings.Join(parts, " · ")
}

// renderSubagentRouteReceipts 渲染有界回执行：最多 8 行、总字节 ≤1 KB；
// 超出部分折叠成一行计数，父代理知道「还有多少没显示」，而不是被静默截断。
// 整批都没有路由信息时返回 nil（未启用路由的批次不产生额外噪音）。
func renderSubagentRouteReceipts(reports []SubagentResult) []string {
	routed := false
	for _, report := range reports {
		if !report.Route.empty() {
			routed = true
			break
		}
	}
	if !routed {
		return nil
	}
	lines := make([]string, 0, maxSubagentRouteReceiptLines+1)
	total := 0
	suppressed := 0
	for _, report := range reports {
		line := report.Route.line(report.ID)
		if len(lines) >= maxSubagentRouteReceiptLines || total+len(line) > maxSubagentRouteReceiptBytes {
			suppressed++
			continue
		}
		total += len(line)
		lines = append(lines, line)
	}
	if suppressed > 0 {
		lines = append(lines, fmt.Sprintf("  route: +%d more (receipt budget)", suppressed))
	}
	return lines
}
