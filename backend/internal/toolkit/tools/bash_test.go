package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

type fakeExecuter struct {
	result CommandExecutionResult
	err    error
}

func (f fakeExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error) {
	return f.result, f.err
}

type inspectExecuter struct {
	result      CommandExecutionResult
	err         error
	lastConfig  execConfig
	lastTimeout time.Duration
}

type batchInspectExecuter struct {
	commands []string
	timeouts []time.Duration
	failures map[string]error
}

func (f *batchInspectExecuter) Execute(_ context.Context, command string, timeout time.Duration, _ ...ExecOption) (CommandExecutionResult, error) {
	f.commands = append(f.commands, command)
	f.timeouts = append(f.timeouts, timeout)
	err := f.failures[command]
	return CommandExecutionResult{Output: "output for " + command}, err
}

func (f *inspectExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error) {
	cfg := execConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	f.lastConfig = cfg
	f.lastTimeout = timeout
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

func TestBashTool_ExecutesStructuredCommandBatchInOneToolCall(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"workdir": ".",
		"commands": []interface{}{
			map[string]interface{}{"command": "git diff --check"},
			map[string]interface{}{"command": "go test ./internal/agent", "timeout": "2m"},
		},
	})

	if err != nil || !result.Success {
		t.Fatalf("expected successful command batch, result=%#v err=%v", result, err)
	}
	if got := strings.Join(inspector.commands, "|"); got != "git diff --check|go test ./internal/agent" {
		t.Fatalf("unexpected commands: %q", got)
	}
	if len(inspector.timeouts) != 2 || inspector.timeouts[0] != defaultShellCommandTimeout || inspector.timeouts[1] != 2*time.Minute {
		t.Fatalf("unexpected command timeouts: %#v", inspector.timeouts)
	}
	if !strings.Contains(result.Content, "command 1/2 [ok]") || !strings.Contains(result.Content, "command 2/2 [ok]") {
		t.Fatalf("expected stable batch sections, got %q", result.Content)
	}
	if got := result.Metadata["executed_count"]; got != 2 {
		t.Fatalf("expected executed_count=2, got %#v", got)
	}
}

func TestBashTool_CommandBatchContinuesAfterFailureByDefault(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{failures: map[string]error{"first": fmt.Errorf("first failed")}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "first"},
			map[string]interface{}{"command": "second"},
		},
	})

	if err != nil || result.Success {
		t.Fatalf("expected completed batch with reported failure, result=%#v err=%v", result, err)
	}
	if len(inspector.commands) != 2 {
		t.Fatalf("expected second command to run after failure, got %#v", inspector.commands)
	}
	if result.Metadata["failed_count"] != 1 || result.Metadata["executed_count"] != 2 {
		t.Fatalf("unexpected batch metadata: %#v", result.Metadata)
	}
}

func TestBashTool_CommandBatchCanStopOnError(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{failures: map[string]error{"first": fmt.Errorf("first failed")}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"stop_on_error": true,
		"commands": []interface{}{
			map[string]interface{}{"command": "first"},
			map[string]interface{}{"command": "second"},
		},
	})

	if err != nil || result.Success {
		t.Fatalf("expected stopped failed batch, result=%#v err=%v", result, err)
	}
	if len(inspector.commands) != 1 || result.Metadata["executed_count"] != 1 {
		t.Fatalf("expected batch to stop after first command, commands=%#v metadata=%#v", inspector.commands, result.Metadata)
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
	if !strings.Contains(description, "powershell") || !strings.Contains(description, "workdir") || !strings.Contains(description, "裸 cd") || !strings.Contains(description, "head") || !strings.Contains(description, "select-object") {
		t.Fatalf("description should steer models toward workdir and PowerShell head compatibility, got %q", tool.Description())
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
	if !strings.Contains(commandDescription, "workdir") || !strings.Contains(commandDescription, "get-location") || !strings.Contains(commandDescription, "head") || !strings.Contains(commandDescription, "select-object") {
		t.Fatalf("command description should mention workdir, Get-Location, and head guidance, got %q", commandDescription)
	}
	if _, ok := properties["timeout_ms"]; !ok {
		t.Fatalf("expected timeout_ms parameter in execute_shell_command schema: %#v", properties)
	}
}

func TestBashTool_CommandDescriptionMentionsPowerShellHeadCompatibility(t *testing.T) {
	tool := NewBashTool()
	description := strings.ToLower(tool.Description())
	if !strings.Contains(description, "powershell") || !strings.Contains(description, "select-object") {
		t.Fatalf("tool description should mention detected Windows shell guidance, got %q", tool.Description())
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
	if !strings.Contains(commandDescription, "powershell") || !strings.Contains(commandDescription, "head") || !strings.Contains(commandDescription, "select-object") {
		t.Fatalf("bash command description should mention PowerShell head compatibility, got %q", commandDescription)
	}
	if _, ok := properties["timeout_ms"]; !ok {
		t.Fatalf("expected timeout_ms parameter in bash schema: %#v", properties)
	}
	commandsSchema, ok := properties["commands"].(map[string]interface{})
	if !ok || commandsSchema["type"] != "array" {
		t.Fatalf("expected structured commands batch parameter, got %#v", properties["commands"])
	}
}

func TestBashTool_PassesExplicitTimeoutToExecuter(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "npx tsc --noEmit",
		"timeout_ms": 120000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 120*time.Second {
		t.Fatalf("expected explicit timeout to reach executer, got %v", inspector.lastTimeout)
	}
	if got := result.Metadata["timeout_ms"]; got != int64(120000) {
		t.Fatalf("expected timeout_ms metadata, got %#v", got)
	}
}

func TestBashTool_UsesEnvDefaultTimeout(t *testing.T) {
	t.Setenv(shellCommandTimeoutEnv, "90s")
	t.Setenv(shellCommandTimeoutMSEnv, "")
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "npx tsc --noEmit",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 90*time.Second {
		t.Fatalf("expected env default timeout to reach executer, got %v", inspector.lastTimeout)
	}
	if got := result.Metadata["timeout_ms"]; got != int64(90000) {
		t.Fatalf("expected env timeout_ms metadata, got %#v", got)
	}
}

func TestBashTool_GoTestUsesExtendedInferredTimeout(t *testing.T) {
	t.Setenv(shellCommandTimeoutEnv, "")
	t.Setenv(shellCommandTimeoutMSEnv, "")
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "$env:GOMAXPROCS='2'; go test -p 1 ./internal/agent",
	})
	if err != nil || !result.Success {
		t.Fatalf("expected inferred go test timeout, result=%#v err=%v", result, err)
	}
	if inspector.lastTimeout != defaultGoTestCommandTimeout {
		t.Fatalf("expected go test timeout %v, got %v", defaultGoTestCommandTimeout, inspector.lastTimeout)
	}
}

func TestBashTool_ExplicitTimeoutOverridesGoTestInference(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "go test ./...",
		"timeout": "45s",
	})
	if err != nil || !result.Success {
		t.Fatalf("expected explicit timeout, result=%#v err=%v", result, err)
	}
	if inspector.lastTimeout != 45*time.Second {
		t.Fatalf("expected explicit 45s timeout, got %v", inspector.lastTimeout)
	}
}

func TestBashTool_NonStringTimeoutFallsBackToDefault(t *testing.T) {
	t.Setenv(shellCommandTimeoutEnv, "45s")
	t.Setenv(shellCommandTimeoutMSEnv, "")
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "pwsh --version",
		"timeout": 30,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success with default timeout, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 45*time.Second {
		t.Fatalf("expected non-string timeout to fall back to default, got %v", inspector.lastTimeout)
	}
}

func TestBashTool_ExplicitTimeoutOverridesEnvDefault(t *testing.T) {
	t.Setenv(shellCommandTimeoutEnv, "90s")
	t.Setenv(shellCommandTimeoutMSEnv, "")
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "npx tsc --noEmit",
		"timeout_ms": 120000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 120*time.Second {
		t.Fatalf("expected explicit timeout to override env default, got %v", inspector.lastTimeout)
	}
}

func TestBashTool_RejectsInvalidTimeout(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "npx tsc --noEmit",
		"timeout_ms": 0,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil || !strings.Contains(result.Error.Error(), "timeout_ms 参数无效") {
		t.Fatalf("expected timeout validation error, got success=%v error=%v", result.Success, result.Error)
	}
}

func TestBashTool_EnvDefaultTimeoutStopsRealWindowsCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific real shell timeout verification")
	}
	shell := runtimeexecutor.DefaultUserShell()
	if shell.Type == runtimeexecutor.ShellTypeCmd {
		t.Skip("test requires PowerShell or pwsh for millisecond sleep")
	}
	t.Setenv(shellCommandTimeoutEnv, "")
	t.Setenv(shellCommandTimeoutMSEnv, "100")

	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "Start-Sleep -Milliseconds 500; Write-Output done",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected real command to time out, got success with output %q", result.Content)
	}
	if !runtimeerrors.Is(result.Error, runtimeerrors.ErrToolTimeout) {
		t.Fatalf("expected structured timeout error, got %v", result.Error)
	}
	if got := result.Metadata["timeout_ms"]; got != int64(100) {
		t.Fatalf("expected timeout_ms metadata from env default, got %#v", got)
	}
	if got := result.Metadata["timeout_requested_ms"]; got != int64(100) {
		t.Fatalf("expected requested timeout metadata from env default, got %#v", got)
	}
	if got := result.Metadata["timeout_source"]; got != "tool_default" {
		t.Fatalf("expected tool_default timeout source, got %#v", got)
	}
}

func TestBashTool_ExplicitTimeoutOverridesEnvDefaultForRealWindowsCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific real shell timeout verification")
	}
	shell := runtimeexecutor.DefaultUserShell()
	if shell.Type == runtimeexecutor.ShellTypeCmd {
		t.Skip("test requires PowerShell or pwsh for millisecond sleep")
	}
	t.Setenv(shellCommandTimeoutEnv, "")
	t.Setenv(shellCommandTimeoutMSEnv, "100")

	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "Start-Sleep -Milliseconds 500; Write-Output done",
		"timeout_ms": 5000,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected real command to complete with explicit timeout, got error %v output %q", result.Error, result.Content)
	}
	if !strings.Contains(result.Content, "done") {
		t.Fatalf("expected command output to contain done, got %q", result.Content)
	}
	if got := result.Metadata["timeout_ms"]; got != int64(5000) {
		t.Fatalf("expected explicit timeout_ms metadata, got %#v", got)
	}
}

func TestBuildCommandExecutionMetadata_RecordsSelectedShell(t *testing.T) {
	metadata := buildCommandExecutionMetadata("git status --short", nil, CommandExecutionResult{
		Output:            "ok",
		TotalBytes:        2,
		RetainedBytes:     2,
		ShellType:         "pwsh",
		ShellPath:         `C:\Program Files\PowerShell\7\pwsh.exe`,
		TotalLines:        1,
		OmittedBytes:      0,
		Truncated:         false,
		CaptureLimitBytes: 0,
	})

	if got := metadata["shell_type"]; got != "pwsh" {
		t.Fatalf("expected shell_type=pwsh, got %#v", got)
	}
	if got := metadata["shell_path"]; got != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("expected shell_path to be preserved, got %#v", got)
	}
	if got := metadata["shell_display"]; got != `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)` {
		t.Fatalf("expected shell_display to be preserved, got %#v", got)
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
		Output:                "Total output lines: 200\nTotal output bytes: 9000\n\nhead\n\n[exec output truncated at capture limit: omitted 4000 bytes from the middle]\n\ntail",
		Truncated:             true,
		TotalBytes:            9000,
		TotalLines:            200,
		RetainedBytes:         5000,
		OmittedBytes:          4000,
		CaptureLimitBytes:     4096,
		RawOutputArtifactPath: `C:\temp\shell-output\toolkit\git_123.txt`,
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
	if got := result.Metadata["captured_output_bytes"]; got != 5000 {
		t.Fatalf("expected captured_output_bytes=5000, got %#v", got)
	}
	if got := result.Metadata["output_capture_limit_bytes"]; got != 4096 {
		t.Fatalf("expected output_capture_limit_bytes=4096, got %#v", got)
	}
	if got := result.Metadata["omitted_output_bytes"]; got != 4000 {
		t.Fatalf("expected omitted_output_bytes=4000, got %#v", got)
	}
	if got := result.Metadata["capture_limit_reached"]; got != true {
		t.Fatalf("expected capture_limit_reached=true, got %#v", got)
	}
	if got := result.Metadata["output_capture_complete"]; got != false {
		t.Fatalf("expected output_capture_complete=false, got %#v", got)
	}
	if got := result.Metadata["raw_output_artifact_path"]; got != `C:\temp\shell-output\toolkit\git_123.txt` {
		t.Fatalf("expected raw_output_artifact_path to be preserved, got %#v", got)
	}
}

func TestBashTool_AnnotatesDisabledCaptureMetadata(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{
		Output:               "full-output",
		TotalBytes:           11,
		TotalLines:           1,
		RetainedBytes:        11,
		CaptureLimitDisabled: true,
	}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":            "echo hello",
		"disable_output_cap": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if got := result.Metadata["output_capture_limit_disabled"]; got != true {
		t.Fatalf("expected output_capture_limit_disabled=true, got %#v", got)
	}
	if got := result.Metadata["output_capture_complete"]; got != true {
		t.Fatalf("expected output_capture_complete=true, got %#v", got)
	}
	if _, exists := result.Metadata["output_capture_limit_bytes"]; exists {
		t.Fatalf("did not expect output_capture_limit_bytes when disabled, got %#v", result.Metadata)
	}
}

func TestBashTool_AnnotatesArtifactErrorMetadata(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{
		Output:                 "partial-output",
		Truncated:              true,
		TotalBytes:             2048,
		RetainedBytes:          1024,
		OmittedBytes:           1024,
		CaptureLimitBytes:      1024,
		RawOutputArtifactError: "disk full",
	}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.Metadata["raw_output_artifact_error"]; got != "disk full" {
		t.Fatalf("expected raw_output_artifact_error=disk full, got %#v", got)
	}
}

func TestBashTool_PassesOutputCaptureOptionsToExecuter(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":          "echo hello",
		"output_bytes_cap": 8192,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if !inspector.lastConfig.hasOutputBytesCap || inspector.lastConfig.outputBytesCap != 8192 {
		t.Fatalf("expected output_bytes_cap to reach executer, got %+v", inspector.lastConfig)
	}
	if inspector.lastConfig.disableOutputCap {
		t.Fatalf("did not expect disableOutputCap, got %+v", inspector.lastConfig)
	}
}

func TestBashTool_RejectsConflictingOutputCaptureOptions(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":            "echo hello",
		"output_bytes_cap":   8192,
		"disable_output_cap": true,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected validation failure, got success with metadata %#v", result.Metadata)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "不能与 disable_output_cap 同时设置") {
		t.Fatalf("expected conflict error, got %#v", result.Error)
	}
}

func TestBashTool_IgnoresNullOutputCaptureOptions(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":            "echo hello",
		"output_bytes_cap":   nil,
		"disable_output_cap": nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastConfig.hasOutputBytesCap {
		t.Fatalf("did not expect output_bytes_cap when null, got %+v", inspector.lastConfig)
	}
	if inspector.lastConfig.disableOutputCap {
		t.Fatalf("did not expect disable_output_cap when null, got %+v", inspector.lastConfig)
	}
}

func TestFriendlyHintFor_WindowsHeadPipeline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific guidance")
	}
	hint := friendlyHintFor(
		`git diff -- internal/gateway/handlers/admin_config.go |head -200`,
		`head : The term 'head' is not recognized as a name of a cmdlet`,
		fmt.Errorf("exit status 1"),
		"",
	)
	if !strings.Contains(hint, "Select-Object -First 200") {
		t.Fatalf("expected head guidance, got %q", hint)
	}
}

func TestFriendlyHintFor_PathNotFoundIncludesWorkdirCandidates(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	candidate := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	hint := friendlyHintFor(
		`git diff -- "frontend/src/pages/setting/runtime.yaml"`,
		`cannot find the file specified`,
		fmt.Errorf("exit status 1"),
		workdir,
	)
	if !strings.Contains(hint, "workdir=") {
		t.Fatalf("expected workdir guidance, got %q", hint)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
	if !strings.Contains(hint, "frontend/src/pages/setting/runtime.yaml") {
		t.Fatalf("expected quoted path token in hint, got %q", hint)
	}
}

func TestEnsureLargeHistoryOutputArtifact_PersistsCompleteLargeOutput(t *testing.T) {
	artifactRoot := t.TempDir()
	t.Setenv("AICLI_SHELL_OUTPUT_ARTIFACT_DIR", "")
	capture := runtimeexecutor.CombinedOutputCapture{
		Output:     strings.Repeat("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 400),
		TotalBytes: 400 * len("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n"),
	}

	artifactPath, artifactErr := ensureLargeHistoryOutputArtifact(capture, "", nil, "toolkit", "git diff", artifactRoot)
	if artifactErr != nil {
		t.Fatalf("did not expect artifact error, got %v", artifactErr)
	}
	if strings.TrimSpace(artifactPath) == "" {
		t.Fatal("expected artifact path for large complete output")
	}
	if !strings.HasPrefix(artifactPath, filepath.Join(artifactRoot, "toolkit")) {
		t.Fatalf("expected artifact under session root %q, got %q", artifactRoot, artifactPath)
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != capture.Output {
		t.Fatal("expected artifact to preserve full output")
	}
}

func filepathIsAbs(p string) bool {
	return len(p) > 0 && (p[0] == '/' || (len(p) > 1 && p[1] == ':'))
}
