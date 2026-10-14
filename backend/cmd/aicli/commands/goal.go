package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

const goalPersistenceRequiredMessage = runtimegoal.PersistenceRequiredMessage

func handleGoalCommand(session *ChatSession, command string) bool {
	arg := strings.TrimSpace(extractCommandArgument(command))
	switch strings.ToLower(arg) {
	case "", "status", "--json":
		printGoalStatus(session, arg == "--json")
		return false
	case "clear":
		if err := requireGoalPersistence(session); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		// deferred storage：新会话尚未落盘时没有可清除的持久化行，按已清除返回。
		if session.runtimeSessionUnpersisted {
			fmt.Println("Goal 已清除")
			return false
		}
		updated, err := runtimegoal.NewMetadataStore().ClearPersistent(context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session))
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		fmt.Println("Goal 已清除")
		return false
	case "pause":
		return updateGoalStatusCommand(session, runtimegoal.StatusPaused, "Goal 已暂停", "user", "")
	case "resume":
		return updateGoalStatusCommand(session, runtimegoal.StatusActive, "Goal 已恢复", "user", "")
	case "complete":
		return updateGoalStatusCommand(session, runtimegoal.StatusComplete, "Goal 已标记完成", "user", "")
	}

	if fields := strings.Fields(arg); len(fields) > 0 && strings.EqualFold(fields[0], "budget") {
		rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
		rest, jsonOutput := stripJSONOption(rest)
		message, err := applyGoalBudgetCommand(session, rest)
		if err != nil {
			if jsonOutput {
				fmt.Println(formatCommandError("错误: "+err.Error(), true))
				return false
			}
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if jsonOutput {
			goal, ok, goalErr := currentSessionGoal(session)
			if goalErr != nil || !ok || goal == nil {
				fmt.Println("当前会话未设置 goal")
				return false
			}
			fmt.Println(marshalIndentedJSON(map[string]interface{}{"goal": goal}))
			return false
		}
		fmt.Println(message)
		if goal, ok, goalErr := currentSessionGoal(session); goalErr == nil && ok && goal != nil {
			fmt.Println(formatGoalSummary(*goal))
		}
		return false
	}

	objective, jsonOutput := stripJSONOption(arg)
	if jsonOutput {
		fmt.Println(formatCommandError("错误: /goal --json 仅支持查看当前 goal", true))
		return false
	}
	if err := prepareGoalMutationPersist(session); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	goal, err := runtimegoal.NewSessionGoal(currentRuntimeSessionID(session), objective, time.Now())
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if goal.TokenBudget <= 0 && session.goalDefaultTokenBudget > 0 {
		goal.TokenBudget = session.goalDefaultTokenBudget
	}
	store := runtimegoal.NewMetadataStore()
	replaced := ""
	if existing, ok, err := store.Get(session.RuntimeSession); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	} else if ok && existing != nil {
		replaced = fmt.Sprintf("（已替换原 %s goal）", existing.Status)
	}
	updated, err := store.PutPersistent(context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), goal, runtimegoal.MutationUser)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	resetGoalAccountingBaseline(session)
	fmt.Printf("Goal 已设置%s\n", replaced)
	fmt.Println(formatGoalSummary(goal))
	if err := sendGoalObjectiveRequest(session, objective); err != nil {
		fmt.Printf("错误: %v\n", err)
	}
	return false
}

func sendGoalObjectiveRequest(session *ChatSession, objective string) error {
	objective = strings.TrimSpace(objective)
	if !shouldSendGoalObjectiveRequest(session, objective) {
		return nil
	}
	response, err := sendMessage(session, objective)
	if err != nil {
		return err
	}
	finishSuccessfulChatSend(session, response, session.NoInteractive)
	return nil
}

func shouldSendGoalObjectiveRequest(session *ChatSession, objective string) bool {
	if session == nil || strings.TrimSpace(objective) == "" {
		return false
	}
	if session.cancelCtx == nil {
		return false
	}
	if session.ChatExecutor != nil {
		return true
	}
	if session.Adapter != nil && strings.TrimSpace(session.Model) != "" {
		return true
	}
	if session.LocalRuntimeHost != nil {
		return true
	}
	return false
}

func printGoalStatus(session *ChatSession, jsonOutput bool) {
	goal, ok, err := currentSessionGoal(session)
	if err != nil {
		if jsonOutput {
			fmt.Println(formatCommandError("错误: "+err.Error(), true))
			return
		}
		fmt.Printf("错误: %v\n", err)
		return
	}
	if !ok || goal == nil {
		if jsonOutput {
			fmt.Println(marshalIndentedJSON(map[string]interface{}{
				"goal": nil,
			}))
			return
		}
		fmt.Println("当前会话未设置 goal")
		return
	}
	if jsonOutput {
		fmt.Println(marshalIndentedJSON(map[string]interface{}{
			"goal": goal,
		}))
		return
	}
	fmt.Println(formatGoalSummary(*goal))
}

func updateGoalStatusCommand(session *ChatSession, status runtimegoal.Status, message string, completedBy string, completionSummary string) bool {
	if err := prepareGoalMutationPersist(session); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	store := runtimegoal.NewMetadataStore()
	goal, ok, err := store.Get(session.RuntimeSession)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if !ok || goal == nil {
		fmt.Println("当前会话未设置 goal")
		return false
	}
	if err := validateGoalStatusTransition(goal.Status, status); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	now := time.Now()
	goal.Status = status
	goal.UpdatedAt = now
	if status == runtimegoal.StatusComplete {
		goal.CompletedAt = &now
		goal.CompletedBy = strings.TrimSpace(completedBy)
		goal.CompletionSummary = strings.TrimSpace(completionSummary)
	} else {
		goal.CompletedAt = nil
		goal.CompletedBy = ""
		goal.CompletionSummary = ""
	}
	updated, err := store.PutPersistent(context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationUser)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	fmt.Println(message)
	fmt.Println(formatGoalSummary(*goal))
	if status == runtimegoal.StatusActive {
		// /goal resume：请求一次隐藏续跑（主循环下一轮迭代消费；对齐 Codex
		// 的"置 active 立即续跑"语义）。
		requestGoalContinuationKick(session)
	}
	return false
}

func validateGoalStatusTransition(from runtimegoal.Status, to runtimegoal.Status) error {
	return runtimegoal.ValidateStatusTransition(from, to)
}

// applyGoalBudgetCommand 处理 `/goal budget [n]`：查看 / 设置 / 清除当前 goal 的
// token 预算（n=0 清除；正整数设置）。预算已耗尽（budget_limited）时把预算提高
// 到超过已用会自动恢复为 active 并请求续跑（对齐 Codex 的"必须提高预算"语义）。
func applyGoalBudgetCommand(session *ChatSession, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if err := prepareGoalMutationPersist(session); err != nil {
		return "", err
	}
	store := runtimegoal.NewMetadataStore()
	goal, ok, err := store.Get(session.RuntimeSession)
	if err != nil {
		return "", err
	}
	if !ok || goal == nil {
		return "", fmt.Errorf("当前会话未设置 goal")
	}
	if arg == "" {
		if goal.TokenBudget > 0 {
			return fmt.Sprintf("Token 预算: %d / %d（已用/预算）", goal.TokensUsed, goal.TokenBudget), nil
		}
		return "当前 goal 未设置 token 预算", nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 0 {
		return "", fmt.Errorf("预算需要非负整数（0 表示清除），得到 %q", arg)
	}
	goal.TokenBudget = n
	goal.UpdatedAt = time.Now()
	message := fmt.Sprintf("Token 预算已设置: %d", n)
	if n == 0 {
		message = "Token 预算已清除"
	}
	reactivated := false
	if n > 0 && goal.Status == runtimegoal.StatusBudgetLimited && n > goal.TokensUsed {
		goal.Status = runtimegoal.StatusActive
		message += "；预算已高于已用，目标已恢复为 active"
		reactivated = true
	}
	updated, err := store.PutPersistent(context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationUser)
	if err != nil {
		return "", err
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return "", err
	}
	if reactivated {
		requestGoalContinuationKick(session)
	}
	return message, nil
}

func currentSessionGoal(session *ChatSession) (*runtimegoal.SessionGoal, bool, error) {
	if session == nil || session.RuntimeSession == nil {
		return nil, false, nil
	}
	return runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
}

func requireGoalPersistence(session *ChatSession) error {
	if session == nil || session.RuntimeSession == nil || session.SessionManager == nil {
		return fmt.Errorf(goalPersistenceRequiredMessage)
	}
	return nil
}

// prepareGoalMutationPersist 保证目标写入前会话已落盘。Deferred 存储下，新会话
// 的首个输入若是 /goal 命令（没有普通消息先触发 ensureChatRuntimeSessionPersisted），
// PutPersistent 会因 session row 不存在而失败——实测（ConPTY，2026-10-14）表现为
// "错误: /goal 的当前参数或状态形式不受支持"。
func prepareGoalMutationPersist(session *ChatSession) error {
	if err := requireGoalPersistence(session); err != nil {
		return err
	}
	return ensureChatRuntimeSessionPersisted(session)
}

func formatGoalSummary(goal runtimegoal.SessionGoal) string {
	lines := []string{
		fmt.Sprintf("Goal: %s", goal.Status),
		"Objective: " + goal.Objective,
	}
	if goal.TokenBudget > 0 || goal.TokensUsed > 0 {
		if goal.TokenBudget > 0 {
			lines = append(lines, fmt.Sprintf("Tokens: %d / %d", goal.TokensUsed, goal.TokenBudget))
		} else {
			lines = append(lines, fmt.Sprintf("Tokens: %d", goal.TokensUsed))
		}
	}
	if goal.CompletedBy != "" {
		lines = append(lines, "Completed by: "+goal.CompletedBy)
	}
	if goal.CompletionSummary != "" {
		lines = append(lines, "Summary: "+goal.CompletionSummary)
	}
	if !goal.UpdatedAt.IsZero() {
		lines = append(lines, "Updated: "+goal.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	return strings.Join(lines, "\n")
}
