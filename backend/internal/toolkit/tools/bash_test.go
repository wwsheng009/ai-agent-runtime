package tools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

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
	result     CommandExecutionResult
	err        error
	lastConfig execConfig
}

func (f *inspectExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error) {
	cfg := execConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	f.lastConfig = cfg
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

func TestFriendlyHintFor_WindowsHeadPipeline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific guidance")
	}
	hint := friendlyHintFor(
		`git diff -- internal/gateway/handlers/admin_config.go | head -200`,
		`head : The term 'head' is not recognized as a name of a cmdlet`,
		fmt.Errorf("exit status 1"),
	)
	if !strings.Contains(hint, "Select-Object -First 200") {
		t.Fatalf("expected head guidance, got %q", hint)
	}
}

func TestEnsureLargeHistoryOutputArtifact_PersistsCompleteLargeOutput(t *testing.T) {
	artifactRoot := t.TempDir()
	t.Setenv("AICLI_SHELL_OUTPUT_ARTIFACT_DIR", artifactRoot)
	capture := runtimeexecutor.CombinedOutputCapture{
		Output:     strings.Repeat("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 400),
		TotalBytes: 400 * len("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n"),
	}

	artifactPath, artifactErr := ensureLargeHistoryOutputArtifact(capture, "", nil, "toolkit", "git diff")
	if artifactErr != nil {
		t.Fatalf("did not expect artifact error, got %v", artifactErr)
	}
	if strings.TrimSpace(artifactPath) == "" {
		t.Fatal("expected artifact path for large complete output")
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
