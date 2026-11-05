package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
	tool := newTestBashTool()
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
	tool := newTestBashTool()
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
	tool := newTestBashTool()
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

func TestBashJobFirstPreservesShellUnavailableErrorCode(t *testing.T) {
	runtime := &fakeShellJobRuntime{
		wait: ShellJobSnapshot{ID: "job_shell_unavailable", Status: "failed"},
		output: ShellJobOutput{
			Status:    "failed",
			Message:   "no usable shell passed the probe",
			ErrorCode: string(runtimeerrors.ErrShellUnavailable),
		},
	}
	tool := newTestBashTool()
	tool.SetShellJobRuntime(runtime)

	result, err := tool.Execute(toolctx.WithSessionID(context.Background(), "session-job"), map[string]interface{}{
		"command": "echo never",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("shell-unavailable job must fail: %+v", result)
	}
	if got := result.Metadata[toolresult.MetadataErrorCodeKey]; got != string(runtimeerrors.ErrShellUnavailable) {
		t.Fatalf("error code = %#v, want SHELL_UNAVAILABLE", got)
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "shell.path") || !strings.Contains(next, "strictProbe") {
		t.Fatalf("next action does not explain shell repair: %q", next)
	}
}

func TestProductionBashToolRequiresJobRuntime(t *testing.T) {
	result, err := NewBashTool().Execute(context.Background(), map[string]interface{}{
		"command": "echo should-not-run",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("shell without a job runtime must fail closed: %+v", result)
	}
	if result.Metadata["failure_class"] != "job_runtime_missing" {
		t.Fatalf("failure_class = %#v", result.Metadata["failure_class"])
	}
}

func TestProductionBashToolRejectsCommandBatch(t *testing.T) {
	result, err := NewBashTool().Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{map[string]interface{}{"command": "echo first"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("command batch must be rejected: %+v", result)
	}
	if result.Metadata["failure_class"] != "batch_unsupported" {
		t.Fatalf("failure_class = %#v", result.Metadata["failure_class"])
	}
}
