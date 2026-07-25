package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TodosTool 任务管理工具
type TodosTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	mu      sync.Mutex
	storage string
	root    string
	memory  map[string][]TodoItem
}

// TodoItem 任务项
type TodoItem struct {
	Content     string `json:"content"`
	Status      string `json:"status"` // pending, in_progress, completed
	ActiveForm  string `json:"active_form"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

// TodoList 任务列表
type TodoList struct {
	Items []TodoItem `json:"items"`
}

type todoUpdateSummary struct {
	Added         int
	StatusChanged int
	Unchanged     int
	Removed       int
}

type todoItemUpdate struct {
	Label          string
	PreviousStatus string
}

// NewTodosTool 创建任务管理工具
func NewTodosTool() *TodosTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type":        "array",
				"description": "任务列表，每个任务包含 content（任务描述）、status（状态：pending/in_progress/completed）、active_form（执行时显示的文本）。同一时间只能有一个任务为 in_progress：开始新任务前先把上一个 in_progress 标为 completed 或 pending。若误传多个 in_progress，会自动保留最后一项并将其余降为 pending。如果任务很多或描述很长，请拆分为多个更小的 todos 调用，每次只聚焦一组相关任务，避免一次性生成超长结构化参数。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{
							"type":        "string",
							"description": "任务描述（祈使句，如 '运行测试'）。若任务说明较长，请拆分为更小的任务条目，每次只聚焦一个任务。",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"pending", "in_progress", "completed"},
							"description": "任务状态：pending / in_progress / completed。整个列表中最多只能有一个 in_progress。",
						},
						"active_form": map[string]interface{}{
							"type":        "string",
							"description": "执行时显示的文本（如 '运行测试中'）。若内容较长，请尽量简短并与 content 保持一致。",
						},
					},
					"required": []string{"content", "status", "active_form"},
				},
			},
		},
		"required": []string{"todos"},
	}

	return &TodosTool{
		BaseTool: toolkit.NewBaseTool(
			"todos",
			"创建和管理结构化任务列表，用于跟踪复杂多步骤任务。状态：pending（未开始）、in_progress（进行中）、completed（已完成）。同一时间只能有一个任务为 in_progress；若误传多个 in_progress，会自动保留最后一项为进行中、其余降为 pending。切换任务时仍应先完成/挂起当前项再开新项。若任务列表较长，请拆分为多个更小的 todos 调用，每次只聚焦一组相关任务。",
			"1.1.0",
			parameters,
			true,
		),
		memory: make(map[string][]TodoItem),
	}
}

func (t *TodosTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: false,
	}
}

// Execute 实现 Tool 接口
func (t *TodosTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	storagePath, ownerKey := t.storageForContext(ctx)

	// 解析任务列表
	todosRaw, ok := params["todos"].([]interface{})
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("todos 参数缺失或类型错误"),
		}, nil
	}

	// 解析每个任务
	newTodos := make([]TodoItem, 0, len(todosRaw))
	for i, todoRaw := range todosRaw {
		todoMap, ok := todoRaw.(map[string]interface{})
		if !ok {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("todos[%d] 不是有效的对象", i),
			}, nil
		}

		content, ok := todoMap["content"].(string)
		if !ok || content == "" {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("todos[%d].content 缺失或为空", i),
			}, nil
		}

		status, ok := todoMap["status"].(string)
		if !ok || (status != "pending" && status != "in_progress" && status != "completed") {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("todos[%d].status 必须是 pending、in_progress 或 completed", i),
			}, nil
		}

		activeForm, ok := todoMap["active_form"].(string)
		if !ok || activeForm == "" {
			// 如果没有 active_form，使用 content
			activeForm = content
		}

		now := time.Now().Unix()
		newTodos = append(newTodos, TodoItem{
			Content:    content,
			Status:     status,
			ActiveForm: activeForm,
			UpdatedAt:  now,
		})
	}

	// Soft-heal multi in_progress: keep the last active item, demote extras to
	// pending. Models often flip several tasks to in_progress when switching
	// focus; failing hard burns a turn for an easy schema repair.
	newTodos, multiInProgressHealed, demotedInProgress := healMultipleInProgressTodos(newTodos)

	previousTodos, loadErr := t.loadTodos(storagePath, ownerKey)

	// 为新任务设置创建时间，为完成的任务设置完成时间
	now := time.Now().Unix()
	for i := range newTodos {
		if newTodos[i].CreatedAt == 0 {
			newTodos[i].CreatedAt = now
		}
		if newTodos[i].Status == "completed" && newTodos[i].CompletedAt == 0 {
			newTodos[i].CompletedAt = now
		}
	}

	updates, removedTodos, updateSummary := compareTodoLists(previousTodos, newTodos)

	// 保存到文件
	storageMode, err := t.saveTodos(storagePath, ownerKey, newTodos)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("保存任务列表失败: %w", err),
		}, nil
	}

	// 统计
	pending := 0
	inProgress := 0
	completed := 0
	for _, todo := range newTodos {
		switch todo.Status {
		case "pending":
			pending++
		case "in_progress":
			inProgress++
		case "completed":
			completed++
		}
	}

	// 构建结果
	result := formatTodosResult(newTodos, updates, removedTodos, updateSummary, loadErr, pending, inProgress, completed, multiInProgressHealed, demotedInProgress)

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    result,
		Metadata: buildTodosResultMetadata(ctx, newTodos, storageMode, pending, inProgress, completed, multiInProgressHealed, demotedInProgress),
	}, nil
}

func buildTodosResultMetadata(ctx context.Context, todos []TodoItem, storageMode string, pending, inProgress, completed int, multiInProgressHealed bool, demotedInProgress []string) map[string]interface{} {
	metadata := map[string]interface{}{
		"total":        len(todos),
		"pending":      pending,
		"in_progress":  inProgress,
		"completed":    completed,
		"todos":        todos,
		"storage_mode": storageMode,
		"session_id":   toolctx.SessionID(ctx),
		"goal_id":      toolctx.GoalID(ctx),
	}
	if multiInProgressHealed {
		metadata["multi_in_progress_healed"] = true
		metadata["demoted_in_progress"] = demotedInProgress
		metadata["demoted_in_progress_count"] = len(demotedInProgress)
		metadata[toolresult.MetadataNextActionKey] = "Continue the single remaining in_progress task. When switching focus, mark the current task completed/pending before starting another; do not submit multiple in_progress items."
	}
	return metadata
}

// healMultipleInProgressTodos keeps the last in_progress item and demotes any
// earlier ones to pending. Returns the healed list, whether healing occurred,
// and the demoted contents (stable order).
func healMultipleInProgressTodos(todos []TodoItem) ([]TodoItem, bool, []string) {
	inProgressIndexes := make([]int, 0, 2)
	for i, todo := range todos {
		if todo.Status == "in_progress" {
			inProgressIndexes = append(inProgressIndexes, i)
		}
	}
	if len(inProgressIndexes) <= 1 {
		return todos, false, nil
	}
	// Keep the last in_progress (most recent focus in the submitted list).
	keep := inProgressIndexes[len(inProgressIndexes)-1]
	demoted := make([]string, 0, len(inProgressIndexes)-1)
	for _, idx := range inProgressIndexes {
		if idx == keep {
			continue
		}
		todos[idx].Status = "pending"
		demoted = append(demoted, todos[idx].Content)
	}
	return todos, true, demoted
}

func compareTodoLists(previous, current []TodoItem) ([]todoItemUpdate, []TodoItem, todoUpdateSummary) {
	updates := make([]todoItemUpdate, len(current))
	matchedPrevious := make([]bool, len(previous))
	summary := todoUpdateSummary{}

	for i, todo := range current {
		if idx := findMatchingTodo(previous, matchedPrevious, todo.Content, todo.Status); idx >= 0 {
			matchedPrevious[idx] = true
			updates[i] = todoItemUpdate{Label: "保持", PreviousStatus: previous[idx].Status}
			summary.Unchanged++
			continue
		}
		if idx := findMatchingTodo(previous, matchedPrevious, todo.Content, ""); idx >= 0 {
			matchedPrevious[idx] = true
			updates[i] = todoItemUpdate{Label: "状态变更", PreviousStatus: previous[idx].Status}
			summary.StatusChanged++
			continue
		}
		updates[i] = todoItemUpdate{Label: "新增"}
		summary.Added++
	}

	removed := make([]TodoItem, 0)
	for i, todo := range previous {
		if matchedPrevious[i] {
			continue
		}
		removed = append(removed, todo)
		summary.Removed++
	}

	return updates, removed, summary
}

func findMatchingTodo(items []TodoItem, matched []bool, content, status string) int {
	for i, item := range items {
		if matched[i] || item.Content != content {
			continue
		}
		if status == "" || item.Status == status {
			return i
		}
	}
	return -1
}

func formatTodosResult(todos []TodoItem, updates []todoItemUpdate, removed []TodoItem, summary todoUpdateSummary, loadErr error, pending, inProgress, completed int, multiInProgressHealed bool, demotedInProgress []string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "任务列表已更新: %d 待处理, %d 进行中, %d 已完成", pending, inProgress, completed)
	builder.WriteString("\n")
	if multiInProgressHealed {
		fmt.Fprintf(&builder, "已自动修复: 检测到多个 in_progress，已保留最后一项为进行中，并将其余 %d 项降为 pending", len(demotedInProgress))
		if len(demotedInProgress) > 0 {
			fmt.Fprintf(&builder, "（%s）", strings.Join(demotedInProgress, "; "))
		}
		builder.WriteString("。next_action: 继续当前唯一 in_progress 任务；切换任务时先 completed/pending 再开新项，不要一次提交多个 in_progress。\n")
	}
	if loadErr != nil {
		fmt.Fprintf(&builder, "任务列表更新状态: 已保存；旧列表读取失败，无法计算差异: %v", loadErr)
	} else {
		fmt.Fprintf(&builder, "任务列表更新状态: 新增 %d, 状态变更 %d, 保持 %d, 移除 %d", summary.Added, summary.StatusChanged, summary.Unchanged, summary.Removed)
	}
	builder.WriteString("\n当前任务列表:")
	if len(todos) == 0 {
		builder.WriteString("\n(空)")
	} else {
		for i, todo := range todos {
			update := todoItemUpdate{}
			if loadErr == nil && i < len(updates) {
				update = updates[i]
			}
			fmt.Fprintf(&builder, "\n%d. [%s] %s", i+1, todoStatusLabel(todo.Status), todo.Content)
			if label := todoUpdateLabel(todo.Status, update); label != "" {
				fmt.Fprintf(&builder, " (%s)", label)
			}
		}
	}
	if len(removed) > 0 {
		builder.WriteString("\n已移除任务:")
		for _, todo := range removed {
			fmt.Fprintf(&builder, "\n- [%s] %s", todoStatusLabel(todo.Status), todo.Content)
		}
	}
	return builder.String()
}

func todoUpdateLabel(currentStatus string, update todoItemUpdate) string {
	switch update.Label {
	case "状态变更":
		return fmt.Sprintf("状态变更: %s -> %s", todoStatusLabel(update.PreviousStatus), todoStatusLabel(currentStatus))
	case "新增", "保持":
		return update.Label
	default:
		return ""
	}
}

func todoStatusLabel(status string) string {
	switch status {
	case "pending":
		return "待处理"
	case "in_progress":
		return "进行中"
	case "completed":
		return "已完成"
	default:
		return status
	}
}

func (t *TodosTool) storageForContext(ctx context.Context) (string, string) {
	if explicit := strings.TrimSpace(t.storage); explicit != "" {
		return explicit, "explicit:" + filepath.Clean(explicit)
	}
	sessionID := toolctx.SessionID(ctx)
	goalID := toolctx.GoalID(ctx)
	sessionSegment := todoOwnerSegment(sessionID, "no-session")
	goalSegment := todoOwnerSegment(goalID, "no-goal")
	root := strings.TrimSpace(t.root)
	if root == "" {
		root = filepath.Join(os.TempDir(), "ai-agent-runtime", "todos")
	}
	path := filepath.Join(root, sessionSegment, goalSegment+".json")
	return path, sessionID + "\x00" + goalID
}

func todoOwnerSegment(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}
	if segment := strings.Trim(builder.String(), "_"); segment != "" {
		if segment == value {
			return segment
		}
		digest := sha256.Sum256([]byte(value))
		return fmt.Sprintf("%s-%x", segment, digest[:6])
	}
	return fallback
}

// loadTodos 加载任务列表
func (t *TodosTool) loadTodos(storagePath, ownerKey string) ([]TodoItem, error) {
	if err := t.checkPath(runtimeexecutor.OpRead, storagePath); err != nil {
		if isSandboxPermissionError(err) {
			return cloneTodoItems(t.memory[ownerKey]), nil
		}
		return nil, err
	}
	data, err := os.ReadFile(storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []TodoItem{}, nil
		}
		return nil, err
	}

	var list TodoList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}

	return cloneTodoItems(list.Items), nil
}

// saveTodos 保存任务列表
func (t *TodosTool) saveTodos(storagePath, ownerKey string, todos []TodoItem) (string, error) {
	if err := t.checkPath(runtimeexecutor.OpWrite, storagePath); err != nil {
		if isSandboxPermissionError(err) {
			if t.memory == nil {
				t.memory = make(map[string][]TodoItem)
			}
			t.memory[ownerKey] = cloneTodoItems(todos)
			return "memory", nil
		}
		return "", err
	}
	list := TodoList{Items: todos}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(storagePath), 0o755); err != nil {
		return "", err
	}

	if err := os.WriteFile(storagePath, data, 0644); err != nil {
		return "", err
	}
	if t.memory == nil {
		t.memory = make(map[string][]TodoItem)
	}
	t.memory[ownerKey] = cloneTodoItems(todos)
	return "file", nil
}

func cloneTodoItems(items []TodoItem) []TodoItem {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]TodoItem, len(items))
	copy(cloned, items)
	return cloned
}

func isSandboxPermissionError(err error) bool {
	if err == nil {
		return false
	}
	var runtimeErr *runtimeerrors.RuntimeError
	return stderrors.As(err, &runtimeErr) && runtimeErr.Code == runtimeerrors.ErrAgentPermission
}
