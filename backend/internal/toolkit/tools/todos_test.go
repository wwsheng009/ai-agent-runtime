package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestTodosTool(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]interface{}
		wantError bool
	}{
		{
			name: "create todo list",
			params: map[string]interface{}{
				"todos": []interface{}{
					map[string]interface{}{
						"content":     "Task 1",
						"status":      "pending",
						"active_form": "Doing Task 1",
					},
					map[string]interface{}{
						"content":     "Task 2",
						"status":      "in_progress",
						"active_form": "Doing Task 2",
					},
					map[string]interface{}{
						"content":     "Task 3",
						"status":      "completed",
						"active_form": "Doing Task 3",
					},
				},
			},
			wantError: false,
		},
		{
			name: "multiple in_progress should fail",
			params: map[string]interface{}{
				"todos": []interface{}{
					map[string]interface{}{
						"content":     "Task 1",
						"status":      "in_progress",
						"active_form": "Doing Task 1",
					},
					map[string]interface{}{
						"content":     "Task 2",
						"status":      "in_progress",
						"active_form": "Doing Task 2",
					},
				},
			},
			wantError: true,
		},
		{
			name:      "missing todos",
			params:    map[string]interface{}{},
			wantError: true,
		},
		{
			name: "invalid status",
			params: map[string]interface{}{
				"todos": []interface{}{
					map[string]interface{}{
						"content":     "Task 1",
						"status":      "invalid_status",
						"active_form": "Doing Task 1",
					},
				},
			},
			wantError: true,
		},
		{
			name: "missing content",
			params: map[string]interface{}{
				"todos": []interface{}{
					map[string]interface{}{
						"status":      "pending",
						"active_form": "Doing Task 1",
					},
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewTodosTool()
			result, err := tool.Execute(context.Background(), tt.params)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantError {
				if result.Success {
					t.Error("expected error but got success")
				}
				return
			}

			if !result.Success {
				t.Errorf("unexpected failure: %v", result.Error)
				return
			}

			t.Logf("Result: %s", result.Content)
		})
	}
}

func TestTodosTool_Interface(t *testing.T) {
	tool := NewTodosTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "todos" {
		t.Errorf("expected name 'todos', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("todos tool should support direct call")
	}
}

func TestTodosTool_OutputIncludesListAndUpdateState(t *testing.T) {
	tool := NewTodosTool()
	tool.storage = filepath.Join(t.TempDir(), "todos.json")

	first, err := tool.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{
				"content":     "Task 1",
				"status":      "pending",
				"active_form": "Doing Task 1",
			},
			map[string]interface{}{
				"content":     "Task 2",
				"status":      "in_progress",
				"active_form": "Doing Task 2",
			},
		},
	})
	if err != nil {
		t.Fatalf("first execute failed: %v", err)
	}
	if !first.Success {
		t.Fatalf("first execute returned failure: %v", first.Error)
	}
	for _, want := range []string{
		"任务列表更新状态: 新增 2, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] Task 1 (新增)",
		"2. [进行中] Task 2 (新增)",
	} {
		if !strings.Contains(first.Content, want) {
			t.Fatalf("expected first output to contain %q, got:\n%s", want, first.Content)
		}
	}

	second, err := tool.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{
				"content":     "Task 1",
				"status":      "completed",
				"active_form": "Doing Task 1",
			},
			map[string]interface{}{
				"content":     "Task 3",
				"status":      "pending",
				"active_form": "Doing Task 3",
			},
		},
	})
	if err != nil {
		t.Fatalf("second execute failed: %v", err)
	}
	if !second.Success {
		t.Fatalf("second execute returned failure: %v", second.Error)
	}
	for _, want := range []string{
		"任务列表更新状态: 新增 1, 状态变更 1, 保持 0, 移除 1",
		"1. [已完成] Task 1 (状态变更: 待处理 -> 已完成)",
		"2. [待处理] Task 3 (新增)",
		"已移除任务:",
		"- [进行中] Task 2",
	} {
		if !strings.Contains(second.Content, want) {
			t.Fatalf("expected second output to contain %q, got:\n%s", want, second.Content)
		}
	}
}

func TestTodosTool_DescriptionGuidesSplittingLargeLists(t *testing.T) {
	tool := NewTodosTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "多个更小") {
		t.Fatalf("expected todos tool description to guide splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	todosSchema, ok := props["todos"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected todos schema in properties, got %#v", props)
	}
	todosDesc, _ := todosSchema["description"].(string)
	if !strings.Contains(todosDesc, "拆分") || !strings.Contains(todosDesc, "超长") {
		t.Fatalf("expected todos description to guide splitting, got %q", todosDesc)
	}
}
