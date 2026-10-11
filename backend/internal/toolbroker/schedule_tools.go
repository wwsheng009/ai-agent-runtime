package toolbroker

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// scheduleToolDefinitions 返回自动化任务（Schedule）工具族。
//
// 与 job_*（后台作业）和 team_*/read_task_*（工作流）区分：schedule_* 只登记
// 提醒，到点由原会话处理，不会立即执行任何命令。
func scheduleToolDefinitions() []types.ToolDefinition {
	return []types.ToolDefinition{
		{
			Name: ToolScheduleCreate,
			Description: "Create a reminder in the current session that delivers prompt when it becomes due. " +
				"Supply exactly one timing parameter: after_seconds, at, or every_seconds. " +
				"This is not a background job: nothing runs now, and the session processes the message only when the reminder fires.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Reminder content to present when the target becomes due.",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": fmt.Sprintf("Task name of at most %d characters, shown on the task card and in task lists.", schedule.MaxTitleLength),
					},
					"after_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Positive delay in whole seconds (one-shot).",
					},
					"at": map[string]interface{}{
						"type":        "string",
						"description": "Absolute RFC 3339 date-time with an explicit offset (one-shot), for example 2099-01-01T09:00:00+08:00.",
					},
					"every_seconds": map[string]interface{}{
						"type":        "integer",
						"description": fmt.Sprintf("Fixed-rate interval in whole seconds, at least %d, aligned to the creation time.", schedule.MinEveryIntervalSeconds),
					},
				},
				"required": []string{"prompt", "title"},
			},
		},
		{
			Name:        ToolScheduleList,
			Description: "List the active reminders in the current session.",
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name: ToolScheduleUpdate,
			Description: "Change a reminder in place, keeping its id. Supply a new title, prompt, or at most one timing parameter (at or every_seconds); " +
				"omitted fields keep their stored values. To change a relative delay, create a new reminder.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "Schedule id returned by schedule_list.",
					},
					"title": map[string]interface{}{
						"type":        "string",
						"description": fmt.Sprintf("New task name of at most %d characters.", schedule.MaxTitleLength),
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "New reminder content.",
					},
					"at": map[string]interface{}{
						"type":        "string",
						"description": "New absolute RFC 3339 target with an explicit offset.",
					},
					"every_seconds": map[string]interface{}{
						"type":        "integer",
						"description": fmt.Sprintf("New fixed-rate interval in whole seconds, at least %d.", schedule.MinEveryIntervalSeconds),
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        ToolScheduleDelete,
			Description: "Delete a reminder in the current session, active or inactive. Deletion does not retract a reminder message that is already queued.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "Exact schedule id.",
					},
				},
				"required": []string{"id"},
			},
		},
	}
}

func (b *Broker) executeScheduleTool(_ context.Context, sessionID, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if b.Schedule == nil {
		return scheduleError(schedule.CodeInternal, "schedule service is unavailable"), nil, nil
	}
	switch toolName {
	case ToolScheduleCreate:
		return b.executeScheduleCreate(sessionID, args)
	case ToolScheduleList:
		return b.executeScheduleList(sessionID)
	case ToolScheduleUpdate:
		return b.executeScheduleUpdate(sessionID, args)
	case ToolScheduleDelete:
		return b.executeScheduleDelete(sessionID, args)
	default:
		return scheduleError(schedule.CodeInternal, "unknown schedule tool"), nil, nil
	}
}

func (b *Broker) executeScheduleCreate(sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	prompt := stringArg(args, "prompt")
	title := stringArg(args, "title")
	req := schedule.CreateRequest{Prompt: prompt, Title: title}
	if value, ok, err := int64Arg(args, "after_seconds"); err != nil {
		return scheduleError(schedule.CodeInvalidRule, err.Error()), nil, nil
	} else if ok {
		req.AfterSeconds = &value
	}
	if value, ok := args["at"].(string); ok && strings.TrimSpace(value) != "" {
		trimmed := strings.TrimSpace(value)
		req.At = &trimmed
	}
	if value, ok, err := int64Arg(args, "every_seconds"); err != nil {
		return scheduleError(schedule.CodeInvalidRule, err.Error()), nil, nil
	} else if ok {
		req.EverySeconds = &value
	}
	record, err := b.Schedule.Create(sessionID, req)
	if err != nil {
		return scheduleFailure(err), nil, nil
	}
	return schedule.BuildView(record, b.Schedule.Now()), nil, nil
}

func (b *Broker) executeScheduleList(sessionID string) (interface{}, map[string]interface{}, error) {
	records, err := b.Schedule.List(sessionID)
	if err != nil {
		return scheduleFailure(err), nil, nil
	}
	now := b.Schedule.Now()
	views := make([]schedule.View, 0, len(records))
	for _, record := range records {
		views = append(views, schedule.BuildView(record, now))
	}
	return views, nil, nil
}

func (b *Broker) executeScheduleUpdate(sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	id := stringArg(args, "id")
	if id == "" || strings.TrimSpace(id) != id {
		return scheduleError(schedule.CodeInvalidRule, "schedule_update id must be non-empty without surrounding whitespace."), nil, nil
	}
	expected, ok, err := b.Schedule.Get(sessionID, id)
	if err != nil {
		return scheduleFailure(err), nil, nil
	}
	if !ok {
		return map[string]interface{}{"id": id, "updated": false, "code": schedule.CodeNotFound}, nil, nil
	}
	var content schedule.UpdateContent
	if value, ok := args["title"].(string); ok {
		content.Title = &value
	}
	if value, ok := args["prompt"].(string); ok {
		content.Prompt = &value
	}
	var change *schedule.TimingChange
	if value, ok := args["at"].(string); ok && strings.TrimSpace(value) != "" {
		trimmed := strings.TrimSpace(value)
		change = &schedule.TimingChange{Kind: schedule.KindAt, At: &trimmed}
	}
	if value, ok, err := int64Arg(args, "every_seconds"); err != nil {
		return scheduleError(schedule.CodeInvalidRule, err.Error()), nil, nil
	} else if ok {
		if change != nil {
			return scheduleError(schedule.CodeInvalidSelector, "at most one timing parameter is allowed."), nil, nil
		}
		change = &schedule.TimingChange{Kind: schedule.KindEvery, EverySeconds: &value}
	}
	result, err := b.Schedule.Update(sessionID, id, expected, content, change)
	if err != nil {
		return scheduleFailure(err), nil, nil
	}
	if result.Updated && result.Record != nil {
		return schedule.BuildView(*result.Record, b.Schedule.Now()), nil, nil
	}
	if result.Code != "" {
		payload := map[string]interface{}{"id": id, "updated": false, "code": result.Code}
		if result.Message != "" {
			payload["message"] = result.Message
		}
		return payload, nil, nil
	}
	// 无变更（归一化等价）：返回当前视图。
	return schedule.BuildView(expected, b.Schedule.Now()), nil, nil
}

func (b *Broker) executeScheduleDelete(sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	id := stringArg(args, "id")
	if id == "" || strings.TrimSpace(id) != id {
		return scheduleError(schedule.CodeInvalidRule, "schedule_delete id must be non-empty without surrounding whitespace."), nil, nil
	}
	result, err := b.Schedule.Delete(sessionID, id)
	if err != nil {
		return scheduleFailure(err), nil, nil
	}
	if result.Deleted {
		return map[string]interface{}{"id": id, "deleted": true}, nil, nil
	}
	return map[string]interface{}{"id": id, "deleted": false, "code": schedule.CodeNotFound}, nil, nil
}

func scheduleError(code schedule.Code, message string) map[string]interface{} {
	return map[string]interface{}{"code": string(code), "message": message}
}

// scheduleFailure 把 InputError 收敛为结构化错误码；其他失败不外泄存储细节。
func scheduleFailure(err error) map[string]interface{} {
	if input, ok := schedule.IsInputError(err); ok {
		return scheduleError(input.Code, input.Message)
	}
	return scheduleError(schedule.CodeInternal, "The schedule operation failed.")
}

func stringArg(args map[string]interface{}, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

// int64Arg 读取一个 JSON number 的整数参数；非整数返回错误。
func int64Arg(args map[string]interface{}, key string) (int64, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return int64(value), true, nil
	case int64:
		return value, true, nil
	case int:
		return int64(value), true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
}
