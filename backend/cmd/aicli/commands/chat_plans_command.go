package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// chatPlanStore resolves the plan artifact store used by the CLI. Tests inject
// chatPlanArtifactStore; production falls back to the process-wide default
// ($HOME/.aicli/plans, or AICLI_PLANS_DIR).
func chatPlanStore() *planstore.Store {
	if chatPlanArtifactStore != nil {
		return chatPlanArtifactStore
	}
	return planmode.DefaultPlanStore()
}

// handlePlansCommand implements `/plans [id]`: browse the archived plan
// artifacts (status/rounds/timestamps) and open one by id. `/plan status`
// reports the *current session's* plan mode; this command reports the archive.
func handlePlansCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredPlansCommand(session, command), false)
		return false
	}
	fmt.Println(plansCommandText(command))
	return false
}

// executeStructuredPlansCommand is the unified-TTY projection of `/plans`.
func executeStructuredPlansCommand(session *ChatSession, command string) CommandResult {
	return commandTextResult(plansCommandText(command))
}

func plansCommandText(command string) string {
	store := chatPlanStore()
	if store == nil {
		return "错误: plan 归档存储不可用"
	}
	id := strings.TrimSpace(extractCommandArgument(command))
	if id == "" {
		return renderStoredPlanList(store)
	}
	return renderStoredPlanDetail(store, id)
}

func renderStoredPlanList(store *planstore.Store) string {
	records, err := store.List()
	if err != nil {
		return fmt.Sprintf("错误: 读取计划归档失败: %v", err)
	}
	if len(records) == 0 {
		return "尚无已归档计划（进入 plan 模式并评审后会自动归档）"
	}
	lines := []string{
		fmt.Sprintf("已归档计划: %d", len(records)),
		fmt.Sprintf("%-28s %-16s %-4s %-20s %s", "ID", "STATUS", "REV", "UPDATED", "PLAN"),
	}
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%-28s %-16s v%-3d %-20s %s",
			truncatePlanCell(record.ID, 28),
			truncatePlanCell(strings.TrimSpace(string(record.Status)), 16),
			record.Version,
			truncatePlanCell(formatPlanTimestamp(record.UpdatedAt), 20),
			truncatePlanCell(record.PlanPath, 48),
		))
	}
	lines = append(lines, "用法: /plans <id> 查看某个计划的详情与最新正文")
	return strings.Join(lines, "\n")
}

func renderStoredPlanDetail(store *planstore.Store, id string) string {
	record, ok, err := store.Get(id)
	if err != nil {
		return fmt.Sprintf("错误: 读取计划归档失败: %v", err)
	}
	if !ok {
		return fmt.Sprintf("未找到计划: %s（用 /plans 列出全部）", id)
	}
	lines := []string{
		fmt.Sprintf("计划: %s", record.ID),
		fmt.Sprintf("  status: %s", strings.TrimSpace(string(record.Status))),
		fmt.Sprintf("  plan path: %s", record.PlanPath),
		fmt.Sprintf("  session: %s", firstNonEmptyChatValue(record.SessionID, "-")),
		fmt.Sprintf("  project: %s", firstNonEmptyChatValue(record.ProjectPath, "-")),
		fmt.Sprintf("  version: v%d", record.Version),
		fmt.Sprintf("  updated: %s", formatPlanTimestamp(record.UpdatedAt)),
	}
	if record.Title != "" {
		lines = append(lines, "  title: "+record.Title)
	}
	for _, round := range record.Rounds {
		entry := fmt.Sprintf("  v%d: %s", round.Version, round.Decision)
		if round.Source != "" {
			entry += " (" + round.Source + ")"
		}
		if round.Notes != "" {
			entry += " — " + round.Notes
		}
		lines = append(lines, entry)
	}
	if content, err := store.ReadLatest(record.ID); err == nil && len(strings.TrimSpace(string(content))) > 0 {
		lines = append(lines, "", string(content))
	} else if err != nil {
		lines = append(lines, "", fmt.Sprintf("(读取最新版本失败: %v)", err))
	} else {
		lines = append(lines, "", "(该计划尚无快照正文)")
	}
	return strings.Join(lines, "\n")
}

// formatPlanTimestamp keeps the CLI table narrow; the parser accepts RFC3339 and
// falls back to the raw value.
func formatPlanTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if len(value) >= 19 {
		return strings.ReplaceAll(value[:19], "T", " ")
	}
	return value
}

func truncatePlanCell(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return value[:limit]
	}
	return value[:limit-1] + "…"
}
