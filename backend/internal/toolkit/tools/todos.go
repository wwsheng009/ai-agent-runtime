package tools

import (
	"context"
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
	memory  []TodoItem
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
				"description": "任务列表，每个任务包含 content（任务描述）、status（状态：pending/in_progress/completed）、active_form（执行时显示的文本）。如果任务很多或描述很长，请拆分为多个更小的 todos 调用，每次只聚焦一组相关任务，避免一次性生成超长结构化参数。",
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
							"description": "任务状态",
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
			"创建和管理结构化任务列表，用于跟踪复杂多步骤任务。状态：pending（未开始）、in_progress（进行中）、completed（已完成）。同一时间只能有一个任务为 in_progress。若任务列表较长，请拆分为多个更小的 todos 调用，每次只聚焦一组相关任务。",
			"1.0.0",
			parameters,
			true,
		),
		storage: filepath.Join(os.TempDir(), "aicli-todos.json"),
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

	// 验证：同一时间只能有一个 in_progress
	inProgressCount := 0
	for _, todo := range newTodos {
		if todo.Status == "in_progress" {
			inProgressCount++
		}
	}
	if inProgressCount > 1 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("同一时间只能有一个任务为 in_progress，当前有 %d 个", inProgressCount),
		}, nil
	}

	previousTodos, loadErr := t.loadTodos()

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
	storageMode, err := t.saveTodos(newTodos)
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
	result := formatTodosResult(newTodos, updates, removedTodos, updateSummary, loadErr, pending, inProgress, completed)

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    result,
		Metadata: map[string]interface{}{
			"total":        len(newTodos),
			"pending":      pending,
			"in_progress":  inProgress,
			"completed":    completed,
			"todos":        newTodos,
			"storage_mode": storageMode,
		},
	}, nil
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

func formatTodosResult(todos []TodoItem, updates []todoItemUpdate, removed []TodoItem, summary todoUpdateSummary, loadErr error, pending, inProgress, completed int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "任务列表已更新: %d 待处理, %d 进行中, %d 已完成", pending, inProgress, completed)
	builder.WriteString("\n")
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

// loadTodos 加载任务列表
func (t *TodosTool) loadTodos() ([]TodoItem, error) {
	if err := t.checkPath(runtimeexecutor.OpRead, t.storage); err != nil {
		if isSandboxPermissionError(err) {
			return cloneTodoItems(t.memory), nil
		}
		return nil, err
	}
	data, err := os.ReadFile(t.storage)
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
func (t *TodosTool) saveTodos(todos []TodoItem) (string, error) {
	if err := t.checkPath(runtimeexecutor.OpWrite, t.storage); err != nil {
		if isSandboxPermissionError(err) {
			t.memory = cloneTodoItems(todos)
			return "memory", nil
		}
		return "", err
	}
	list := TodoList{Items: todos}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(t.storage), 0o755); err != nil {
		return "", err
	}

	if err := os.WriteFile(t.storage, data, 0644); err != nil {
		return "", err
	}
	t.memory = cloneTodoItems(todos)
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
