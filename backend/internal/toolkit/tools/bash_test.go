package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeExecuter struct {
	result CommandExecutionResult
	err    error
}

func (f fakeExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error) {
	return f.result, f.err
}

func TestBashTool_EmitsMutatedPaths(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":       "echo hello",
		"mutated_paths": []string{"a.txt", "b.txt"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	raw, ok := result.Metadata["mutated_paths"]
	if !ok {
		t.Fatalf("expected mutated_paths metadata, got %#v", result.Metadata)
	}
	paths, ok := raw.([]string)
	if !ok || len(paths) != 2 {
		t.Fatalf("unexpected mutated_paths metadata: %#v", raw)
	}
}

func TestExecuteShellCommandTool_EmitsMutatedPaths(t *testing.T) {
	tool := NewExecuteShellCommandTool()
	tool.BashTool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":       "echo hello",
		"mutated_paths": []string{"x.txt"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	raw, ok := result.Metadata["mutated_paths"]
	if !ok {
		t.Fatalf("expected mutated_paths metadata, got %#v", result.Metadata)
	}
	paths, ok := raw.([]string)
	if !ok || len(paths) != 1 {
		t.Fatalf("unexpected mutated_paths metadata: %#v", raw)
	}
}

func TestExecuteShellCommandTool_DescribesDetectedWindowsShellAndWorkdir(t *testing.T) {
	tool := NewExecuteShellCommandTool()
	description := strings.ToLower(tool.Description())
	if !strings.Contains(description, "powershell") || !strings.Contains(description, "workdir") || !strings.Contains(description, "裸 cd") {
		t.Fatalf("description should steer models toward workdir and away from bare cd, got %q", tool.Description())
	}
	params := tool.Parameters()
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing properties: %#v", params)
	}
	commandSchema, ok := properties["command"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing command schema: %#v", properties)
	}
	commandDescription := strings.ToLower(fmt.Sprint(commandSchema["description"]))
	if !strings.Contains(commandDescription, "workdir") || !strings.Contains(commandDescription, "get-location") {
		t.Fatalf("command description should mention workdir and Get-Location, got %q", commandDescription)
	}
}

func TestBashTool_WorkdirParameter(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello",
		"workdir": "/tmp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
}

func TestResolveWorkdir(t *testing.T) {
	tests := []struct {
		name    string
		workdir string
		wantAbs bool
		wantErr bool
	}{
		{"empty defaults to cwd", "", true, false},
		{"absolute path", "/tmp", true, false},
		{"relative path", "subdir", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveWorkdir(tt.workdir)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveWorkdir() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !filepathIsAbs(got) {
				t.Errorf("resolveWorkdir() = %v, want absolute path", got)
			}
		})
	}
}

func TestBashTool_AnnotatesTruncatedOutputMetadata(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{
		Output:     "Total output lines: 200\nTotal output bytes: 9000\n\nhead\n\n[exec output truncated at capture limit: omitted 4000 bytes from the middle]\n\ntail",
		Truncated:  true,
		TotalBytes: 9000,
		TotalLines: 200,
	}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if got := result.Metadata["output_truncated"]; got != true {
		t.Fatalf("expected output_truncated=true, got %#v", got)
	}
	if got := result.Metadata["total_output_bytes"]; got != 9000 {
		t.Fatalf("expected total_output_bytes=9000, got %#v", got)
	}
	if got := result.Metadata["total_output_lines"]; got != 200 {
		t.Fatalf("expected total_output_lines=200, got %#v", got)
	}
}

func filepathIsAbs(p string) bool {
	return len(p) > 0 && (p[0] == '/' || (len(p) > 1 && p[1] == ':'))
}
