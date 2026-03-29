package tools

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
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

// NewTodosTool 创建任务管理工具
func NewTodosTool() *TodosTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type":        "array",
				"description": "任务列表，每个任务包含 content（任务描述）、status（状态：pending/in_progress/completed）、active_form（执行时显示的文本）",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{
							"type":        "string",
							"description": "任务描述（祈使句，如 '运行测试'）",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"pending", "in_progress", "completed"},
							"description": "任务状态",
						},
						"active_form": map[string]interface{}{
							"type":        "string",
							"description": "执行时显示的文本（如 '运行测试中'）",
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
			"创建和管理结构化任务列表，用于跟踪复杂多步骤任务。状态：pending（未开始）、in_progress（进行中）、completed（已完成）。同一时间只能有一个任务为 in_progress。",
			"1.0.0",
			parameters,
			true,
		),
		storage: filepath.Join(os.TempDir(), "aicli-todos.json"),
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
			Success: false,
			Error:   fmt.Errorf("todos 参数缺失或类型错误"),
		}, nil
	}

	// 解析每个任务
	newTodos := make([]TodoItem, 0, len(todosRaw))
	for i, todoRaw := range todosRaw {
		todoMap, ok := todoRaw.(map[string]interface{})
		if !ok {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("todos[%d] 不是有效的对象", i),
			}, nil
		}

		content, ok := todoMap["content"].(string)
		if !ok || content == "" {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("todos[%d].content 缺失或为空", i),
			}, nil
		}

		status, ok := todoMap["status"].(string)
		if !ok || (status != "pending" && status != "in_progress" && status != "completed") {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("todos[%d].status 必须是 pending、in_progress 或 completed", i),
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
			Success: false,
			Error:   fmt.Errorf("同一时间只能有一个任务为 in_progress，当前有 %d 个", inProgressCount),
		}, nil
	}

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

	// 保存到文件
	storageMode, err := t.saveTodos(newTodos)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("保存任务列表失败: %w", err),
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
	result := fmt.Sprintf("任务列表已更新: %d 待处理, %d 进行中, %d 已完成", pending, inProgress, completed)

	return &toolkit.ToolResult{
		Success: true,
		Content: result,
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
