package commands

import (
	"context"
	"fmt"
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

	objective, jsonOutput := stripJSONOption(arg)
	if jsonOutput {
		fmt.Println(formatCommandError("错误: /goal --json 仅支持查看当前 goal", true))
		return false
	}
	if err := requireGoalPersistence(session); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	goal, err := runtimegoal.NewSessionGoal(currentRuntimeSessionID(session), objective, time.Now())
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
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
	if err := requireGoalPersistence(session); err != nil {
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
	return false
}

func validateGoalStatusTransition(from runtimegoal.Status, to runtimegoal.Status) error {
	return runtimegoal.ValidateStatusTransition(from, to)
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
	if !goal.UpdatedAt.IsZero() {
		lines = append(lines, "Updated: "+goal.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	return strings.Join(lines, "\n")
}
