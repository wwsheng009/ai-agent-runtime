package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
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

// handlePlansCommand implements `/plans [id]` and `/plans reopen <id> [vN]`:
// browse the archived plan artifacts (status/rounds/timestamps), open one by id,
// or restore one snapshot back into the workspace and continue its review.
// `/plan status` reports the *current session's* plan mode; this command reports
// the archive.
func handlePlansCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredPlansCommand(session, command), false)
		return false
	}
	fmt.Println(plansCommandTextForSession(session, command))
	return false
}

// executeStructuredPlansCommand is the unified-TTY projection of `/plans`.
func executeStructuredPlansCommand(session *ChatSession, command string) CommandResult {
	return commandTextResult(plansCommandTextForSession(session, command))
}

// plansCommandText is the session-less projection used by callers that only
// browse the archive (`reopen` needs a session and reports that instead).
func plansCommandText(command string) string {
	return plansCommandTextForSession(nil, command)
}

func plansCommandTextForSession(session *ChatSession, command string) string {
	store := chatPlanStore()
	if store == nil {
		return "错误: plan 归档存储不可用"
	}
	rest := strings.TrimSpace(extractCommandArgument(command))
	if rest == "" {
		return renderStoredPlanList(store)
	}
	if tokens := strings.Fields(rest); len(tokens) > 1 && isPlansReopenKeyword(tokens[0]) {
		return reopenStoredPlan(session, store, tokens[1:])
	}
	return renderStoredPlanDetail(store, rest)
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

// isPlansReopenKeyword reports whether the first /plans argument selects the
// reopen action. Ids are "<project-slug>/<plan-name>", so the keywords cannot
// collide with a real id in practice (and a collision can still be browsed with
// `/plans <id>` because only "keyword + more args" is treated as an action).
func isPlansReopenKeyword(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "reopen", "restore":
		return true
	default:
		return false
	}
}

// parsePlansReopenArgs splits `<id> [vN|--version N] [--force]`.
func parsePlansReopenArgs(args []string) (id string, version int, force bool, err error) {
	idParts := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		token := strings.TrimSpace(args[index])
		if token == "" {
			continue
		}
		switch {
		case token == "--force" || token == "-f":
			force = true
		case token == "--version" || token == "-v":
			if index+1 >= len(args) {
				return "", 0, false, fmt.Errorf("--version 需要一个版本号")
			}
			index++
			value, convErr := strconv.Atoi(strings.TrimSpace(args[index]))
			if convErr != nil || value <= 0 {
				return "", 0, false, fmt.Errorf("版本号必须是正整数: %s", args[index])
			}
			version = value
		case strings.HasPrefix(token, "--version="):
			value, convErr := strconv.Atoi(strings.TrimPrefix(token, "--version="))
			if convErr != nil || value <= 0 {
				return "", 0, false, fmt.Errorf("版本号必须是正整数: %s", token)
			}
			version = value
		case isPlanVersionToken(token):
			value, convErr := strconv.Atoi(strings.TrimPrefix(strings.ToLower(token), "v"))
			if convErr != nil || value <= 0 {
				return "", 0, false, fmt.Errorf("版本号必须是正整数: %s", token)
			}
			version = value
		default:
			idParts = append(idParts, token)
		}
	}
	return strings.Join(idParts, " "), version, force, nil
}

// isPlanVersionToken reports whether token is "<v|V><digits>", e.g. "v2".
func isPlanVersionToken(token string) bool {
	if len(token) < 2 || (token[0] != 'v' && token[0] != 'V') {
		return false
	}
	for _, r := range token[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// reopenStoredPlan restores an archived snapshot into the workspace plan file and
// enters plan mode on it, so a reviewed plan can be picked up again in this or a
// later session. Content safety lives in planmode.ReopenPlan: an identical file
// is left alone, a missing one is created, and a diverged one needs --force.
func reopenStoredPlan(session *ChatSession, store *planstore.Store, args []string) string {
	if session == nil {
		return "错误: 重新评审归档计划需要活动会话"
	}
	id, version, force, err := parsePlansReopenArgs(args)
	if err != nil {
		return fmt.Sprintf("错误: %v", err)
	}
	if id == "" {
		return "用法: /plans reopen <id> [vN] [--force]（用 /plans 列出全部计划）"
	}
	result, err := planmode.ReopenPlan(planmode.ReopenOptions{
		Store:     store,
		Workspace: chatPlanWorkspacePath(session),
		RecordID:  id,
		Version:   version,
		Force:     force,
	})
	if err != nil {
		if errors.Is(err, planmode.ErrReopenConflict) {
			return fmt.Sprintf("未重新评审: %v", err)
		}
		return fmt.Sprintf("错误: 重新评审失败: %v", err)
	}

	mutation, enterErr := enterChatPlanModeWithResult(session, result.DisplayPath)
	if enterErr != nil {
		return fmt.Sprintf("已恢复 %s v%d，但进入 plan mode 失败: %v",
			result.Record.ID, result.Version, enterErr)
	}
	if state := loadChatPlanMode(session); planmode.IsActive(state) {
		saveChatPlanMode(session, planmode.MarkReopened(state, result.Record.ID, result.Version))
	}

	lines := []string{
		fmt.Sprintf("已从归档恢复计划: %s", result.Record.ID),
		fmt.Sprintf("  plan path: %s", result.DisplayPath),
		fmt.Sprintf("  version: v%d（%d 字节）", result.Version, result.Bytes),
	}
	switch {
	case result.Unchanged:
		lines = append(lines, "  内容: 工作区文件与快照一致，未改写")
	case result.Created:
		lines = append(lines, "  内容: 工作区文件不存在，已按快照创建")
	default:
		lines = append(lines, "  内容: 已按快照覆盖工作区文件（--force）")
	}
	lines = append(lines, fmt.Sprintf("  状态: 已进入 plan mode（previous=%s），可继续评审（/plan request_changes <notes> / /plan approve）",
		firstNonEmptyChatValue(mutation.State.PreviousMode, string(runtimepolicy.ModeDefault))))
	if mutation.SyncErr != nil {
		lines = append(lines, fmt.Sprintf("  提示: 运行时同步失败（状态已保存）: %v", mutation.SyncErr))
	}
	return strings.Join(lines, "\n")
}
