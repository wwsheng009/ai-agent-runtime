package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestShellTool_NameDescriptionVersion(t *testing.T) {
	tool := NewShellTool()
	if tool.Name() != "shell" {
		t.Fatalf("expected name shell, got %q", tool.Name())
	}
	if tool.Version() == "" {
		t.Fatal("expected non-empty version")
	}
	desc := tool.Description()
	for _, needle := range []string{"非零", "Exit code", "bash", "execute_shell_command"} {
		if !strings.Contains(desc, needle) {
			t.Fatalf("expected description to mention %q, got %q", needle, desc)
		}
	}
	params := tool.Parameters()
	if params == nil {
		t.Fatal("expected parameters schema")
	}
	props, _ := params["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	if _, ok := props["command"]; !ok {
		t.Fatalf("expected command property, got %#v", props)
	}
	if _, ok := props["commands"]; !ok {
		t.Fatalf("expected commands batch property, got %#v", props)
	}
	if !tool.CanDirectCall() {
		t.Fatal("expected shell tool to allow direct call")
	}
}

func TestShellTool_RealNonZeroExitIsContentSuccess(t *testing.T) {
	tool := NewShellTool()
	command := "exit 1"
	if runtime.GOOS != "windows" {
		command = "false"
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    command,
		"timeout_ms": 10000,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected real non-zero exit to be content success, got error=%v content=%q", result.Error, result.Content)
	}
	if result.Error != nil {
		t.Fatalf("expected nil Error on content success, got %v", result.Error)
	}
	if result.Metadata["exit_code"] != 1 {
		t.Fatalf("expected exit_code=1, got %#v metadata=%#v", result.Metadata["exit_code"], result.Metadata)
	}
	if result.Metadata["non_zero_exit"] != true {
		t.Fatalf("expected non_zero_exit=true, got %#v", result.Metadata["non_zero_exit"])
	}
	if !strings.Contains(result.Content, "Exit code: 1") {
		t.Fatalf("expected Exit code header in content, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Output:") {
		t.Fatalf("expected Output header in content, got %q", result.Content)
	}
}

func TestShellTool_DelegatesHardFailure(t *testing.T) {
	tool := NewShellTool()
	tool.executer = fakeExecuter{err: context.DeadlineExceeded}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo should-timeout",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected hard timeout failure, got %#v", result)
	}
	if result.Error == nil {
		t.Fatal("expected Error on hard failure")
	}
}
