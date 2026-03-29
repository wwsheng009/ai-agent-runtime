package tools

import (
	"context"
	"testing"
	"time"
)

type fakeExecuter struct {
	output string
	err    error
}

func (f fakeExecuter) Execute(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return f.output, f.err
}

func TestBashTool_EmitsMutatedPaths(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{output: "ok"}

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
	tool.BashTool.executer = fakeExecuter{output: "ok"}

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
