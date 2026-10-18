package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

const goalPersistenceRequiredMessage = runtimegoal.PersistenceRequiredMessage

// errGoalNotSet 表示当前会话没有 goal：渲染层各自给出用户可见文案（legacy
// 打印 "当前会话未设置 goal"；结构化面渲染 None 文档）。
var errGoalNotSet = errors.New("goal not set")

// goalStatusChangeTarget 把 /goal pause|resume|complete 的动词映射到目标状态与
// 确认文案（legacy 与结构化统一分派共用的单点，防止两套入口文案/状态漂移）。
func goalStatusChangeTarget(verb string) (runtimegoal.Status, string, bool) {
	switch verb {
	case "pause":
		return runtimegoal.StatusPaused, "Goal 已暂停", true
	case "resume":
		return runtimegoal.StatusActive, "Goal 已恢复", true
	case "complete":
		return runtimegoal.StatusComplete, "Goal 已标记完成", true
	default:
		return "", "", false
	}
}

// applyGoalStatusChange 是 /goal pause|resume|complete 的语义单点（2026-10-18
// 入口收敛）：状态迁移校验、用户态字段规范化、持久化、状态恢复、G4 rearm 与
// 续跑 kick 只存在这一处——legacy handleGoalCommand 与结构化统一分派都经由此
// 函数。此前结构化面缺 rearm 导致 "/goal resume 没有作用"（真机 07:42 复现）。
func applyGoalStatusChange(session *ChatSession, status runtimegoal.Status, message, completedBy, completionSummary string) (runtimegoal.SessionGoal, error) {
	if err := prepareGoalMutationPersist(session); err != nil {
		return runtimegoal.SessionGoal{}, err
	}
	if status == runtimegoal.StatusActive {
		// G4 rearm 闸：resume 是用户的明示继续意图——先清进程内 disarmed 标记，
		// 否则 durable 状态回到 active 也会被 goal_disarmed 决策挡住。
		rearmGoalContinuation(session, "goal_resume_command")
	}
	store := runtimegoal.NewMetadataStore()
	goal, ok, err := store.Get(session.RuntimeSession)
	if err != nil {
		return runtimegoal.SessionGoal{}, err
	}
	if !ok || goal == nil {
		return runtimegoal.SessionGoal{}, errGoalNotSet
	}
	if err := validateGoalStatusTransition(goal.Status, status); err != nil {
		return runtimegoal.SessionGoal{}, err
	}
	runtimegoal.ApplyUserStatusUpdate(goal, status, completedBy, completionSummary, time.Now())
	updated, err := store.PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationUser)
	if err != nil {
		return runtimegoal.SessionGoal{}, err
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return runtimegoal.SessionGoal{}, err
	}
	if status == runtimegoal.StatusActive {
		// /goal resume：请求一次隐藏续跑（主循环下一轮迭代消费；对齐 Codex
		// 的"置 active 立即续跑"语义）。
		requestGoalContinuationKick(session)
	}
	return *goal, nil
}

// clearGoalCore 是 /goal clear 的语义单点。deferred storage 下尚无持久化行时
// 按已清除处理（不为一条 clear 创建空会话行）。
func clearGoalCore(session *ChatSession) error {
	if err := requireGoalPersistence(session); err != nil {
		return err
	}
	if session.runtimeSessionUnpersisted {
		return nil
	}
	updated, err := runtimegoal.NewMetadataStore().ClearPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session))
	if err != nil {
		return err
	}
	return restoreChatStateFromRuntimeSession(session, updated)
}

// setGoalCore 是 /goal <objective> 的语义单点（默认预算/轮数、替换说明、持久化
// 与记账基线重置）。objective 的发送/渲染由各入口承担：legacy 同步调用
// sendGoalObjectiveRequest；结构化面经 SendObjective 在确认 cell 提交后触发。
func setGoalCore(session *ChatSession, objective string) (runtimegoal.SessionGoal, string, error) {
	if err := prepareGoalMutationPersist(session); err != nil {
		return runtimegoal.SessionGoal{}, "", err
	}
	goal, err := runtimegoal.NewSessionGoal(currentRuntimeSessionID(session), objective, time.Now())
	if err != nil {
		return runtimegoal.SessionGoal{}, "", err
	}
	if goal.TokenBudget <= 0 && session.goalDefaultTokenBudget > 0 {
		goal.TokenBudget = session.goalDefaultTokenBudget
	}
	if goal.MaxRounds <= 0 && session.goalDefaultMaxRounds > 0 {
		goal.MaxRounds = session.goalDefaultMaxRounds
	}
	store := runtimegoal.NewMetadataStore()
	replaced := ""
	if existing, ok, err := store.Get(session.RuntimeSession); err != nil {
		return runtimegoal.SessionGoal{}, "", err
	} else if ok && existing != nil {
		replaced = fmt.Sprintf("（已替换原 %s goal）", existing.Status)
	}
	updated, err := store.PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), goal, runtimegoal.MutationUser)
	if err != nil {
		return runtimegoal.SessionGoal{}, "", err
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return runtimegoal.SessionGoal{}, "", err
	}
	resetGoalAccountingBaseline(session)
	return goal, replaced, nil
}

// goalStatusTrailingHints 返回 /goal status 附在摘要后的提示行（G4 disarmed
// 可见性单点）：legacy printGoalStatus 与结构化状态文档共用，避免"一套入口有
// 提示、另一套没有"的漂移（2026-10-18 真机复现项）。
func goalStatusTrailingHints(session *ChatSession, goal *runtimegoal.SessionGoal) []string {
	if session == nil || goal == nil {
		return nil
	}
	if goal.Status == runtimegoal.StatusActive && session.goalContinuationDisarmed.Load() {
		return []string{goalDisarmedStatusHint}
	}
	return nil
}

func handleGoalCommand(session *ChatSession, command string) bool {
	arg := strings.TrimSpace(extractCommandArgument(command))
	switch strings.ToLower(arg) {
	case "", "status", "--json":
		printGoalStatus(session, arg == "--json")
		return false
	case "clear":
		if err := clearGoalCore(session); err != nil {
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
	goal, replaced, err := setGoalCore(session, objective)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
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
	// G4 rearm 闸可见性：提示行由 goalStatusTrailingHints 单点产出，与结构化
	// /goal status 文档共用（防入口漂移）。
	for _, hint := range goalStatusTrailingHints(session, goal) {
		fmt.Println(hint)
	}
}

func updateGoalStatusCommand(session *ChatSession, status runtimegoal.Status, message string, completedBy string, completionSummary string) bool {
	// 语义单点：校验/持久化/rearm/kick 都在 applyGoalStatusChange；本函数只做
	// legacy 文本渲染（结构化统一分派走 executeStructuredGoalStatusChange）。
	goal, err := applyGoalStatusChange(session, status, message, completedBy, completionSummary)
	if err != nil {
		if errors.Is(err, errGoalNotSet) {
			fmt.Println("当前会话未设置 goal")
			return false
		}
		fmt.Printf("错误: %v\n", err)
		return false
	}
	fmt.Println(message)
	fmt.Println(formatGoalSummary(goal))
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
		goal.BlockedReason = ""
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
	if goal.BlockedReason != "" {
		lines = append(lines, "Blocked reason: "+goal.BlockedReason)
	}
	if goal.MaxRounds > 0 {
		lines = append(lines, fmt.Sprintf("Rounds: %d / %d", goal.RoundsStarted, goal.MaxRounds))
	} else if goal.RoundsStarted > 0 {
		lines = append(lines, fmt.Sprintf("Rounds: %d", goal.RoundsStarted))
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
