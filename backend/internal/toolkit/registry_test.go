package toolkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func TestRegistry(t *testing.T) {
	// 创建注册表
	registry := toolkit.NewRegistry()

	// 测试注册 Bash 工具
	bashTool := tools.NewBashTool()
	err := registry.Register(bashTool)
	if err != nil {
		t.Fatalf("Failed to register bash tool: %v", err)
	}

	// 测试获取工具
	tool, ok := registry.Get("bash")
	if !ok {
		t.Fatal("Tool not found")
	}

	if tool.Name() != "bash" {
		t.Errorf("Expected tool name 'bash', got '%s'", tool.Name())
	}

	// 测试列出的工具
	toolsList := registry.List()
	if len(toolsList) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(toolsList))
	}

	// 测试重复注册
	err = registry.Register(bashTool)
	if err == nil {
		t.Error("Expected error when registering duplicate tool")
	}

	// 测试注销
	err = registry.Unregister("bash")
	if err != nil {
		t.Errorf("Failed to unregister tool: %v", err)
	}

	_, ok = registry.Get("bash")
	if ok {
		t.Error("Tool should not exist after unregister")
	}
}

func TestRegistryList_SortsToolsByName(t *testing.T) {
	registry := toolkit.NewRegistry()

	if err := registry.Register(tools.NewWriteTool()); err != nil {
		t.Fatalf("register write: %v", err)
	}
	if err := registry.Register(tools.NewBashTool()); err != nil {
		t.Fatalf("register bash: %v", err)
	}
	if err := registry.Register(tools.NewViewTool()); err != nil {
		t.Fatalf("register view: %v", err)
	}

	toolsList := registry.List()
	if len(toolsList) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(toolsList))
	}
	if toolsList[0].Name() != "bash" || toolsList[1].Name() != "view" || toolsList[2].Name() != "write" {
		t.Fatalf("expected sorted tool list, got %s, %s, %s", toolsList[0].Name(), toolsList[1].Name(), toolsList[2].Name())
	}
}

func TestRegistryExecute_UnwrapsRawArgs(t *testing.T) {
	registry := toolkit.NewRegistry()
	if err := registry.Register(tools.NewBashTool()); err != nil {
		t.Fatalf("register bash: %v", err)
	}
	result, err := registry.Execute(context.Background(), "bash", map[string]interface{}{
		"_raw": `{"command":"echo registry-raw-ok"}`,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %v", result.Error)
	}
	if !strings.Contains(result.Content, "registry-raw-ok") {
		t.Fatalf("expected raw command output, got %q", result.Content)
	}
}

func TestBashTool(t *testing.T) {
	ctx := context.Background()
	bashTool := tools.NewBashTool()

	// 测试参数缺失
	result, err := bashTool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when command parameter is missing")
	}

	// 测试空命令
	result, err = bashTool.Execute(ctx, map[string]interface{}{
		"command": "",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when command is empty")
	}

	// 测试简单命令
	result, err = bashTool.Execute(ctx, map[string]interface{}{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Errorf("Command execution failed: %v", result.Error)
	}

	if result.Content == "" {
		t.Error("Expected output, got empty string")
	}

	if result.Metadata == nil {
		t.Error("Expected metadata")
	}
}

func TestViewTool(t *testing.T) {
	ctx := context.Background()
	viewTool := tools.NewViewTool()

	// 测试参数缺失
	result, err := viewTool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when file_path parameter is missing")
	}

	// 测试读取不存在的文件
	result, err = viewTool.Execute(ctx, map[string]interface{}{
		"file_path": "/nonexistent/file.txt",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure for nonexistent file")
	}
}

func TestEditTool(t *testing.T) {
	ctx := context.Background()
	editTool := tools.NewEditTool()

	// 测试参数缺失
	result, err := editTool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when parameters are missing")
	}

	// 测试空 old_string
	result, err = editTool.Execute(ctx, map[string]interface{}{
		"file_path":   "test.txt",
		"old_string":  "",
		"new_string":  "new content",
		"replace_all": false,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when old_string is empty")
	}
}

func TestWriteTool(t *testing.T) {
	ctx := context.Background()
	writeTool := tools.NewWriteTool()

	// 测试参数缺失
	result, err := writeTool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when parameters are missing")
	}

	// 测试空 file_path
	result, err = writeTool.Execute(ctx, map[string]interface{}{
		"file_path": "",
		"content":   "test content",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected failure when file_path is empty")
	}
}

func TestToolResult(t *testing.T) {
	result := &toolkit.ToolResult{
		Success: true,
		Content: "test content",
		Metadata: map[string]interface{}{
			"key": "value",
		},
	}

	// 测试序列化
	data, err := result.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected non-empty JSON data")
	}

	// 测试反序列化
	result2, err := toolkit.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}

	if result2.Success != result.Success {
		t.Error("Success mismatch after JSON round-trip")
	}

	if result2.Content != result.Content {
		t.Error("Content mismatch after JSON round-trip")
	}
}

func TestExecuteShellCommandToolSchema(t *testing.T) {
	tool := tools.NewExecuteShellCommandTool()
	if tool.Name() != "execute_shell_command" {
		t.Fatalf("expected tool name execute_shell_command, got %s", tool.Name())
	}

	params := tool.Parameters()
	if params == nil {
		t.Fatal("expected parameters, got nil")
	}
	if ap, ok := params["additionalProperties"].(bool); !ok || ap != false {
		t.Fatalf("expected additionalProperties=false, got %v", params["additionalProperties"])
	}
	props, ok := params["properties"].(map[string]interface{})
	if !ok || props["command"] == nil {
		t.Fatalf("expected command property, got %v", params)
	}
}

func TestToolParametersIncludeAdditionalProperties(t *testing.T) {
	bashTool := tools.NewBashTool()
	params := bashTool.Parameters()
	if params == nil {
		t.Fatal("expected parameters, got nil")
	}
	if ap, ok := params["additionalProperties"].(bool); !ok || ap != false {
		t.Fatalf("expected additionalProperties=false, got %v", params["additionalProperties"])
	}
}
