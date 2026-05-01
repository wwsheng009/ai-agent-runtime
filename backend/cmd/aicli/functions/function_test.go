package functions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

type plainTestFunction struct {
	name   string
	output string
}

func (f *plainTestFunction) Name() string { return f.name }

func (f *plainTestFunction) Description() string { return "plain test function" }

func (f *plainTestFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}

func (f *plainTestFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return f.output, nil
}

type richRuntimeToolProvider struct {
	output   string
	metadata map[string]interface{}
}

func (p *richRuntimeToolProvider) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return p.output, nil
}

func (p *richRuntimeToolProvider) ExecuteWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	return p.output, p.metadata, nil
}

func TestFunctionRegistry_ExecuteFunctionWithMeta_FallsBackForPlainFunction(t *testing.T) {
	registry := NewFunctionRegistry()
	registry.Register(&plainTestFunction{name: "plain", output: "ok"})

	output, metadata, err := registry.ExecuteFunctionWithMeta(context.Background(), "plain", nil)
	if err != nil {
		t.Fatalf("ExecuteFunctionWithMeta failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if metadata != nil {
		t.Fatalf("expected nil metadata for plain function, got %#v", metadata)
	}
}

func TestRuntimeToolFunction_ExecuteWithMeta_PreservesProviderMetadata(t *testing.T) {
	fn := NewRuntimeToolFunction(&richRuntimeToolProvider{
		output: "job queued",
		metadata: map[string]interface{}{
			"tool_source": "broker",
			"output_kind": "text",
		},
	}, runtimetools.ToolDescriptor{
		Name:        "background_task",
		Description: "background task",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	output, metadata, err := fn.ExecuteWithMeta(context.Background(), map[string]interface{}{"command": "git status"})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != "job queued" {
		t.Fatalf("expected output job queued, got %q", output)
	}
	if got := metadata["tool_source"]; got != "broker" {
		t.Fatalf("expected tool_source=broker, got %#v", got)
	}
	if got := metadata["output_kind"]; got != "text" {
		t.Fatalf("expected output_kind=text, got %#v", got)
	}
}

func TestFunctionRegistry_GetFunctionSchemas_PreservesDefinitionMetadata(t *testing.T) {
	registry := NewFunctionRegistry()
	registry.Register(NewRuntimeToolFunction(&richRuntimeToolProvider{
		output: "ok",
	}, runtimetools.ToolDescriptor{
		Name:        "apply_patch",
		Description: "apply patch",
		Parameters:  map[string]interface{}{"type": "object"},
		Metadata: map[string]interface{}{
			"freeform": map[string]interface{}{
				"type":   "grammar",
				"syntax": "lark",
			},
		},
	}))

	schemas := registry.GetFunctionSchemas()
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
	metadata, ok := schemas[0]["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %#v", schemas[0]["metadata"])
	}
	freeform, ok := metadata["freeform"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected freeform metadata, got %#v", metadata)
	}
	if got := freeform["syntax"]; got != "lark" {
		t.Fatalf("expected freeform syntax=lark, got %#v", got)
	}
}

type inspectShellExecuter struct {
	output     string
	err        error
	lastConfig execConfig
}

func (e *inspectShellExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error) {
	cfg := execConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	e.lastConfig = cfg
	return e.output, e.err
}

type inspectDetailedShellExecuter struct {
	result     ShellExecutionResult
	err        error
	lastConfig execConfig
}

func (e *inspectDetailedShellExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error) {
	cfg := execConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	e.lastConfig = cfg
	return e.result.Output, e.err
}

func (e *inspectDetailedShellExecuter) ExecuteDetailed(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (ShellExecutionResult, error) {
	cfg := execConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	e.lastConfig = cfg
	return e.result, e.err
}

func TestShellFunction_PassesOutputCaptureOptions(t *testing.T) {
	fn := NewShellFunction()
	inspector := &inspectShellExecuter{output: "ok"}
	fn.SetExecuter(inspector)

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"command":          "git status --short",
		"output_bytes_cap": 8192,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if !inspector.lastConfig.hasOutputBytesCap || inspector.lastConfig.outputBytesCap != 8192 {
		t.Fatalf("expected output_bytes_cap to reach executer, got %+v", inspector.lastConfig)
	}
	if inspector.lastConfig.disableOutputCap {
		t.Fatalf("did not expect disableOutputCap, got %+v", inspector.lastConfig)
	}
}

func TestShellFunction_RejectsConflictingOutputCaptureOptions(t *testing.T) {
	fn := NewShellFunction()
	fn.SetExecuter(&inspectShellExecuter{output: "ok"})

	_, err := fn.Execute(context.Background(), map[string]interface{}{
		"command":            "git status --short",
		"output_bytes_cap":   8192,
		"disable_output_cap": true,
	})
	if err == nil || !strings.Contains(err.Error(), "不能与 disable_output_cap 同时设置") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestShellFunction_IgnoresNullOutputCaptureOptions(t *testing.T) {
	fn := NewShellFunction()
	inspector := &inspectShellExecuter{output: "ok"}
	fn.SetExecuter(inspector)

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"command":            "git status --short",
		"output_bytes_cap":   nil,
		"disable_output_cap": nil,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if inspector.lastConfig.hasOutputBytesCap {
		t.Fatalf("did not expect output_bytes_cap when null, got %+v", inspector.lastConfig)
	}
	if inspector.lastConfig.disableOutputCap {
		t.Fatalf("did not expect disableOutputCap when null, got %+v", inspector.lastConfig)
	}
}

func TestShellFunction_ExecuteWithMeta_PreservesCaptureMetadata(t *testing.T) {
	fn := NewShellFunction()
	inspector := &inspectDetailedShellExecuter{
		result: ShellExecutionResult{
			Output: "diff output",
			Metadata: map[string]interface{}{
				"output_capture_complete":  false,
				"retained_output_bytes":    4096,
				"omitted_output_bytes":     2048,
				"raw_output_artifact_path": `C:\temp\shell-output\function\git_123.txt`,
			},
		},
	}
	fn.SetExecuter(inspector)

	output, metadata, err := fn.ExecuteWithMeta(context.Background(), map[string]interface{}{
		"command":          "git diff --stat",
		"output_bytes_cap": 4096,
	})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != "diff output" {
		t.Fatalf("expected diff output, got %q", output)
	}
	if got := metadata["output_capture_complete"]; got != false {
		t.Fatalf("expected output_capture_complete=false, got %#v", got)
	}
	if got := metadata["raw_output_artifact_path"]; got != `C:\temp\shell-output\function\git_123.txt` {
		t.Fatalf("expected raw_output_artifact_path to be preserved, got %#v", got)
	}
	if !inspector.lastConfig.hasOutputBytesCap || inspector.lastConfig.outputBytesCap != 4096 {
		t.Fatalf("expected output_bytes_cap to reach executer, got %+v", inspector.lastConfig)
	}
}

func TestFriendlyHintForCommand_WindowsHeadPipeline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific guidance")
	}
	hint := friendlyHintForCommand(
		`git diff -- internal/gateway/handlers/admin_config.go | head -200`,
		`head : The term 'head' is not recognized as a name of a cmdlet`,
		context.DeadlineExceeded,
		"",
	)
	if !strings.Contains(hint, "Select-Object -First 200") {
		t.Fatalf("expected head guidance, got %q", hint)
	}
}

func TestFriendlyHintForCommand_FileMissingIncludesWorkdir(t *testing.T) {
	hint := friendlyHintForCommand(
		"cat missing.txt",
		"cannot find the path",
		os.ErrNotExist,
		"E:/projects/ai/ai-agent-runtime/backend",
	)
	if !strings.Contains(hint, "workdir=E:/projects/ai/ai-agent-runtime/backend") {
		t.Fatalf("expected workdir guidance, got %q", hint)
	}
}

func TestFriendlyHintForCommand_FileMissingIncludesSuggestion(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	suggested := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(suggested), 0o755); err != nil {
		t.Fatalf("mkdir suggested path: %v", err)
	}
	if err := os.WriteFile(suggested, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write suggested file: %v", err)
	}

	hint := friendlyHintForCommand(
		"cat frontend/src/pages/setting/runtime.yaml",
		"cannot find the path",
		os.ErrNotExist,
		workdir,
	)
	normalized := strings.ReplaceAll(hint, `\`, `/`)
	if !strings.Contains(normalized, "frontend/src/pages/settings/runtime.yaml") {
		t.Fatalf("expected path suggestion, got %q", hint)
	}
}

func TestFriendlyHintForCommand_FileMissingIncludesQuotedPathSuggestion(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	suggested := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime file.yaml")
	if err := os.MkdirAll(filepath.Dir(suggested), 0o755); err != nil {
		t.Fatalf("mkdir suggested path: %v", err)
	}
	if err := os.WriteFile(suggested, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write suggested file: %v", err)
	}

	hint := friendlyHintForCommand(
		`cat "frontend/src/pages/setting/runtime file.yaml"`,
		"cannot find the path",
		os.ErrNotExist,
		workdir,
	)
	normalized := strings.ReplaceAll(hint, `\`, `/`)
	if !strings.Contains(normalized, "frontend/src/pages/settings/runtime file.yaml") {
		t.Fatalf("expected quoted path suggestion, got %q", hint)
	}
}

func TestShellFunction_DescriptionAndParameters_MentionPowerShellHeadGuidance(t *testing.T) {
	fn := NewShellFunction()
	description := strings.ToLower(fn.Description())
	if !strings.Contains(description, "head") || !strings.Contains(description, "select-object") || !strings.Contains(description, "workdir") {
		t.Fatalf("expected shell function description to mention head guidance and workdir, got %q", fn.Description())
	}

	params := fn.Parameters()
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing properties: %#v", params)
	}
	commandSchema, ok := properties["command"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing command schema: %#v", properties)
	}
	commandDescription := strings.ToLower(commandSchema["description"].(string))
	if !strings.Contains(commandDescription, "head") || !strings.Contains(commandDescription, "select-object") || !strings.Contains(commandDescription, "get-location") {
		t.Fatalf("expected shell command schema to mention PowerShell head guidance, got %q", commandDescription)
	}
}

func TestBuildShellExecutionMetadata_RecordsSelectedShell(t *testing.T) {
	workdir := "E:/projects/ai/ai-agent-runtime/backend"
	metadata := buildShellExecutionMetadata(
		"git status --short",
		"ok",
		runtimeexecutor.CombinedOutputCapture{
			Output:        "ok",
			TotalBytes:    2,
			RetainedBytes: 2,
		},
		"",
		nil,
		runtimeexecutor.Shell{
			Type: runtimeexecutor.ShellTypePwsh,
			Path: `C:\Program Files\PowerShell\7\pwsh.exe`,
		},
		workdir,
	)

	if got := metadata["shell_type"]; got != "pwsh" {
		t.Fatalf("expected shell_type=pwsh, got %#v", got)
	}
	if got := metadata["shell_path"]; got != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("expected shell_path to be preserved, got %#v", got)
	}
	if got := metadata["shell_display"]; got != `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)` {
		t.Fatalf("expected shell_display to be preserved, got %#v", got)
	}
	if got := metadata["workdir"]; got != workdir {
		t.Fatalf("expected workdir to be preserved, got %#v", got)
	}
	if got := metadata["command_length_bytes"]; got != len("git status --short") {
		t.Fatalf("expected command_length_bytes to be preserved, got %#v", got)
	}
}

func TestEnsureLargeHistoryOutputArtifact_PersistsCompleteLargeOutput(t *testing.T) {
	artifactRoot := t.TempDir()
	t.Setenv("AICLI_SHELL_OUTPUT_ARTIFACT_DIR", artifactRoot)
	capture := runtimeexecutor.CombinedOutputCapture{
		Output:     strings.Repeat("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 400),
		TotalBytes: 400 * len("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n"),
	}

	artifactPath, artifactErr := ensureLargeHistoryOutputArtifact(capture, "", nil, "function", "git diff")
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
