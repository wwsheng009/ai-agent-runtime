package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

type fakeShellJobRuntime struct {
	submitCommand string
	wait          ShellJobSnapshot
	output        ShellJobOutput
	forgotten     bool
	cancelled     bool
}

func (f *fakeShellJobRuntime) SubmitShell(_ context.Context, _ string, command, _ string) (string, string, error) {
	f.submitCommand = command
	return "job_fake", "running", nil
}

func (f *fakeShellJobRuntime) WaitShell(_ context.Context, _ string, _ time.Duration) (ShellJobSnapshot, error) {
	return f.wait, nil
}

func (f *fakeShellJobRuntime) ReadShell(_ context.Context, _ string, _ int64, _ int) (ShellJobOutput, error) {
	return f.output, nil
}

func (f *fakeShellJobRuntime) CancelShell(context.Context, string) error {
	f.cancelled = true
	return nil
}

func (f *fakeShellJobRuntime) ForgetShell(context.Context, string, string) error {
	f.forgotten = true
	return nil
}

func TestBashJobFirstReturnsForegroundAndForgetsQuickJob(t *testing.T) {
	exitCode := 0
	runtime := &fakeShellJobRuntime{
		wait: ShellJobSnapshot{ID: "job_fake", Status: "completed", ExitCode: &exitCode},
		output: ShellJobOutput{
			Output:     "job output\n",
			NextOffset: 11,
			Status:     "completed",
			ExitCode:   &exitCode,
		},
	}
	tool := NewBashTool()
	tool.SetShellJobRuntime(runtime)

	result, err := tool.Execute(toolctx.WithSessionID(context.Background(), "session-job"), map[string]interface{}{
		"command": "echo job output",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("job-first quick command failed: %+v", result)
	}
	if runtime.submitCommand != "echo job output" {
		t.Fatalf("submitted command = %q", runtime.submitCommand)
	}
	if !runtime.forgotten {
		t.Fatal("quick completed job was not forgotten")
	}
	if result.Metadata["job_first"] != true || result.Metadata["job_id"] != "job_fake" {
		t.Fatalf("job metadata missing: %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "job output") {
		t.Fatalf("content missing job output: %q", result.Content)
	}
}

func TestBashJobFirstTimeoutReturnsPromotedJob(t *testing.T) {
	runtime := &fakeShellJobRuntime{
		wait: ShellJobSnapshot{ID: "job_slow", Status: "running", TimedOut: true},
		output: ShellJobOutput{
			Output:     "early output\n",
			NextOffset: 13,
			Status:     "running",
		},
	}
	tool := NewBashTool()
	tool.SetShellJobRuntime(runtime)

	result, err := tool.Execute(toolctx.WithSessionID(context.Background(), "session-job"), map[string]interface{}{
		"command":     "long command",
		"timeout_sec": 1,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("promoted result should be success content: %+v", result)
	}
	if runtime.forgotten {
		t.Fatal("timed-out job must not be forgotten")
	}
	if result.Metadata["promoted"] != true || result.Metadata["job_id"] != "job_fake" {
		t.Fatalf("promoted metadata missing: %#v", result.Metadata)
	}
	if result.Metadata["job_output_offset"] != int64(13) {
		t.Fatalf("cursor = %#v", result.Metadata["job_output_offset"])
	}
	if !strings.Contains(result.Content, "job_fake") {
		t.Fatalf("content missing job id: %q", result.Content)
	}
}

func TestBashJobFirstRequiresOwner(t *testing.T) {
	runtime := &fakeShellJobRuntime{}
	tool := NewBashTool()
	tool.SetShellJobRuntime(runtime)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("ownerless job was accepted: %+v", result)
	}
	if got := result.Metadata["failure_class"]; got != "owner_required" {
		t.Fatalf("failure_class = %#v", got)
	}
}
