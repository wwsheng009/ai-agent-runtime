package tools

import (
	"context"
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
