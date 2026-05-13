package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclitools"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	GetToolName                = "get_goal"
	UpdateToolName             = "update_goal"
	PersistenceRequiredMessage = "当前会话未启用持久化，无法设置 goal"
)

func Capabilities() []aiclitools.Capability {
	return []aiclitools.Capability{
		{
			Name:        GetToolName,
			Description: "Get the current goal for this chat session, including status and usage fields.",
			Parameters:  EmptyParameters(),
			Metadata:    CapabilityMetadata(),
			Exposure: []aiclitools.ExposurePath{
				aiclitools.ExposureShared,
				aiclitools.ExposureActor,
				aiclitools.ExposureRuntimeServer,
			},
			Execute: executeGetGoalCapability,
		},
		{
			Name:        UpdateToolName,
			Description: "Mark the current goal complete only when the objective has actually been achieved and no required work remains.",
			Parameters:  UpdateParameters(),
			Metadata:    CapabilityMetadata(),
			Exposure: []aiclitools.ExposurePath{
				aiclitools.ExposureShared,
				aiclitools.ExposureActor,
				aiclitools.ExposureRuntimeServer,
			},
			Execute: executeUpdateGoalCapability,
		},
	}
}

func CapabilityRegistry() *aiclitools.Registry {
	return aiclitools.NewRegistry(Capabilities()...)
}

func CapabilityMetadata() map[string]interface{} {
	return map[string]interface{}{
		"source": "aicli_goal",
	}
}

func EmptyParameters() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"properties":           map[string]interface{}{},
		"additionalProperties": false,
	}
}

func UpdateParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{string(StatusComplete)},
				"description": "Required. Must be complete. Pause, resume, and clear are controlled by the user.",
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "Required. Concise evidence-based summary explaining why the goal is complete.",
			},
		},
		"required":             []string{"status", "summary"},
		"additionalProperties": false,
	}
}

func executeGetGoalCapability(ctx context.Context, toolSession aiclitools.ToolSessionContext, args map[string]interface{}) (aiclitools.ToolResult, error) {
	runtimeSession := runtimeSessionFromToolContext(toolSession)
	if runtimeSession == nil {
		return aiclitools.ToolResult{Output: marshalIndentedJSON(map[string]interface{}{"goal": nil})}, nil
	}
	goal, ok, err := NewMetadataStore().Get(runtimeSession)
	if err != nil {
		return aiclitools.ToolResult{}, err
	}
	if !ok || goal == nil {
		return aiclitools.ToolResult{Output: marshalIndentedJSON(map[string]interface{}{"goal": nil})}, nil
	}
	return aiclitools.ToolResult{Output: marshalIndentedJSON(map[string]interface{}{
		"goal":             goal,
		"remaining_tokens": RemainingTokens(*goal),
	})}, nil
}

func executeUpdateGoalCapability(ctx context.Context, toolSession aiclitools.ToolSessionContext, args map[string]interface{}) (aiclitools.ToolResult, error) {
	if err := requireToolPersistence(toolSession); err != nil {
		return aiclitools.ToolResult{}, err
	}
	status := strings.TrimSpace(stringArg(args, "status"))
	if status != string(StatusComplete) {
		return aiclitools.ToolResult{}, fmt.Errorf("update_goal can only mark the existing goal complete; pause, resume, and clear are controlled by the user")
	}
	summary := strings.TrimSpace(stringArg(args, "summary"))
	if summary == "" {
		return aiclitools.ToolResult{}, fmt.Errorf("update_goal requires a completion summary")
	}

	runtimeSession := runtimeSessionFromToolContext(toolSession)
	store := NewMetadataStore()
	goal, ok, err := store.Get(runtimeSession)
	if err != nil {
		return aiclitools.ToolResult{}, err
	}
	if !ok || goal == nil {
		return aiclitools.ToolResult{}, fmt.Errorf("current session has no goal")
	}
	if goal.Status == StatusPaused {
		return aiclitools.ToolResult{}, fmt.Errorf("update_goal cannot complete a paused goal; resume the goal before model completion")
	}
	if err := ValidateStatusTransition(goal.Status, StatusComplete); err != nil {
		return aiclitools.ToolResult{}, err
	}
	now := time.Now()
	goal.Status = StatusComplete
	goal.UpdatedAt = now
	goal.CompletedAt = &now
	goal.CompletedBy = "model"
	goal.CompletionSummary = summary

	updated, err := store.PutPersistent(ctx, toolSession.SessionStorage(), toolSession.SessionID(), *goal, MutationModel)
	if err != nil {
		return aiclitools.ToolResult{}, err
	}
	refreshToolSession(ctx, toolSession, updated)
	return aiclitools.ToolResult{Output: marshalIndentedJSON(map[string]interface{}{
		"goal":                     goal,
		"remaining_tokens":         RemainingTokens(*goal),
		"completion_budget_report": CompletionBudgetReport(*goal),
	})}, nil
}

func runtimeSessionFromToolContext(toolSession aiclitools.ToolSessionContext) *runtimechat.Session {
	if toolSession == nil {
		return nil
	}
	return toolSession.RuntimeSession()
}

func requireToolPersistence(toolSession aiclitools.ToolSessionContext) error {
	if toolSession == nil || toolSession.RuntimeSession() == nil || toolSession.SessionStorage() == nil {
		return fmt.Errorf(PersistenceRequiredMessage)
	}
	return nil
}

func refreshToolSession(ctx context.Context, toolSession aiclitools.ToolSessionContext, updated *runtimechat.Session) {
	if toolSession == nil || updated == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = toolSession.RefreshRuntimeSession(ctx, updated)
}

func stringArg(args map[string]interface{}, key string) string {
	if len(args) == 0 {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

func RemainingTokens(goal SessionGoal) interface{} {
	if goal.TokenBudget <= 0 {
		return nil
	}
	remaining := goal.TokenBudget - goal.TokensUsed
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

func CompletionBudgetReport(goal SessionGoal) string {
	parts := make([]string, 0, 2)
	if goal.TokenBudget > 0 {
		parts = append(parts, fmt.Sprintf("tokens used: %d of %d", goal.TokensUsed, goal.TokenBudget))
	}
	if goal.TimeUsedSeconds > 0 {
		parts = append(parts, fmt.Sprintf("time used: %d seconds", goal.TimeUsedSeconds))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Goal achieved. Report final budget usage to the user: " + strings.Join(parts, "; ") + "."
}

func ValidateStatusTransition(from Status, to Status) error {
	if from == to {
		return nil
	}
	switch from {
	case StatusActive:
		if to == StatusPaused || to == StatusComplete {
			return nil
		}
	case StatusPaused:
		if to == StatusActive || to == StatusComplete {
			return nil
		}
	case StatusBudgetLimited:
		if to == StatusActive || to == StatusComplete {
			return nil
		}
	case StatusComplete:
		return fmt.Errorf("goal 已完成；请使用 /goal clear 清除，或 /goal <objective> 设置新目标")
	}
	return fmt.Errorf("不支持的 goal 状态转换: %s -> %s", from, to)
}

func marshalIndentedJSON(value interface{}) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}
