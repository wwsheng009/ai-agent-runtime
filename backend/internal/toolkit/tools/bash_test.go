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
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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

type parallelBatchExecuter struct {
	started chan string
	release chan struct{}
}

func (f *batchInspectExecuter) Execute(_ context.Context, command string, timeout time.Duration, _ ...ExecOption) (CommandExecutionResult, error) {
	f.commands = append(f.commands, command)
	f.timeouts = append(f.timeouts, timeout)
	err := f.failures[command]
	return CommandExecutionResult{Output: "output for " + command}, err
}

func (f *parallelBatchExecuter) Execute(ctx context.Context, command string, _ time.Duration, _ ...ExecOption) (CommandExecutionResult, error) {
	select {
	case f.started <- command:
	case <-ctx.Done():
		return CommandExecutionResult{}, ctx.Err()
	}
	select {
	case <-f.release:
		return CommandExecutionResult{Output: "output for " + command}, nil
	case <-ctx.Done():
		return CommandExecutionResult{}, ctx.Err()
	}
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

func TestBashTool_EmptyOptionalCommandBatchFallsBackToSingleCommand(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":  "git status",
		"commands": []interface{}{},
		"workdir":  ".",
	})

	if err != nil || !result.Success {
		t.Fatalf("expected empty optional batch to use command, result=%#v err=%v", result, err)
	}
	if got := strings.Join(inspector.commands, "|"); got != "git status" {
		t.Fatalf("expected single command execution, got %q", got)
	}
	if strings.Contains(result.Content, "command 1/1") {
		t.Fatalf("expected direct command output, got batch wrapper %q", result.Content)
	}
}

func TestBashTool_EmptyCommandBatchWithoutSingleCommandStillFails(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "unexpected"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []map[string]interface{}{},
	})

	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil || !strings.Contains(result.Error.Error(), "commands 参数不能为空") {
		t.Fatalf("expected empty commands validation error, got %#v", result)
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
	if result.Error == nil || !strings.Contains(result.Error.Error(), "失败摘要") {
		t.Fatalf("expected batch failure summary in error, got %#v", result.Error)
	}
	if result.Metadata["partial_failure"] != true || result.Metadata["succeeded_count"] != 1 {
		t.Fatalf("expected partial_failure with succeeded_count=1, got %#v", result.Metadata)
	}
	rawFailed, ok := result.Metadata[toolresult.MetadataFailedItemsKey].([]map[string]interface{})
	if !ok || len(rawFailed) != 1 {
		t.Fatalf("expected structured failed_items, got %#v", result.Metadata[toolresult.MetadataFailedItemsKey])
	}
	if rawFailed[0]["ref"] != "first" {
		t.Fatalf("expected failed command ref=first, got %#v", rawFailed[0])
	}
	if idx, ok := rawFailed[0]["index"].(int); !ok || idx != 0 {
		t.Fatalf("expected failed item index=0, got %#v", rawFailed[0]["index"])
	}
	// Extractor should surface the same rows from source-side metadata.
	items := toolresult.ExtractFailedItems(result.Metadata)
	if len(items) != 1 || items[0].Ref != "first" {
		t.Fatalf("expected ExtractFailedItems to use source failed_items, got %#v", items)
	}
}

func TestBashTool_CommandBatchNonZeroExitIsContentSuccess(t *testing.T) {
	tool := NewBashTool()
	tool.executer = &batchInspectExecuter{failures: map[string]error{
		"first":  fmt.Errorf("exit status 1"),
		"second": fmt.Errorf("exit status 2"),
	}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "first"},
			map[string]interface{}{"command": "second"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected batch of non-zero exits to succeed, got error: %v content=%q", result.Error, result.Content)
	}
	if result.Metadata["failed_count"] != 0 {
		t.Fatalf("expected failed_count=0 for process exits, got %#v", result.Metadata)
	}
	if result.Metadata["non_zero_exit_count"] != 2 {
		t.Fatalf("expected non_zero_exit_count=2, got %#v", result.Metadata["non_zero_exit_count"])
	}
	if !strings.Contains(result.Content, "exit_nonzero") {
		t.Fatalf("expected exit_nonzero section status, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Exit code: 1") || !strings.Contains(result.Content, "Exit code: 2") {
		t.Fatalf("expected per-command Exit code headers, got %q", result.Content)
	}
}

func TestFormatShellCommandContent(t *testing.T) {
	content := formatShellCommandContent(1, "pwsh", `E:\work`, 1230*time.Millisecond, false, "boom\n")
	if !strings.Contains(content, "Exit code: 1") {
		t.Fatalf("missing exit code: %q", content)
	}
	if !strings.Contains(content, "Shell: pwsh") || !strings.Contains(content, `Workdir: E:\work`) {
		t.Fatalf("missing shell/workdir: %q", content)
	}
	if !strings.Contains(content, "Wall time: 1.23s") {
		t.Fatalf("missing wall time: %q", content)
	}
	if !strings.Contains(content, "Timed out: false") {
		t.Fatalf("missing timed out: %q", content)
	}
	if !strings.Contains(content, "Output:\nboom\n") {
		t.Fatalf("missing output body: %q", content)
	}
}

func TestIsHardShellExecutionError(t *testing.T) {
	if isHardShellExecutionError(fmt.Errorf("exit status 1")) {
		t.Fatal("exit status must not be hard")
	}
	if isHardShellExecutionError(fmt.Errorf("命令执行失败: exit status 2\n提示: x")) {
		t.Fatal("wrapped exit status must not be hard")
	}
	if !isHardShellExecutionError(fmt.Errorf("exec: file not found")) {
		t.Fatal("launch failure must be hard")
	}
	timeoutErr := runtimeerrors.Wrap(runtimeerrors.ErrToolTimeout, "execution timed out", context.DeadlineExceeded)
	if !isHardShellExecutionError(timeoutErr) {
		t.Fatal("timeout must be hard")
	}
}

func TestBashTool_RealNonZeroExitIsContentSuccess(t *testing.T) {
	tool := NewBashTool()
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
	if !result.Success {
		t.Fatalf("expected real non-zero exit to be content success, got error=%v content=%q", result.Error, result.Content)
	}
	if result.Metadata["exit_code"] != 1 {
		t.Fatalf("expected exit_code=1, got %#v metadata=%#v", result.Metadata["exit_code"], result.Metadata)
	}
	if !strings.Contains(result.Content, "Exit code: 1") {
		t.Fatalf("expected Exit code header in content, got %q", result.Content)
	}
}

func TestBashTool_MissingCommandIncludesParseErrorHint(t *testing.T) {
	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"_parse_error": "unexpected end of JSON input",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected missing command failure, got %#v", result)
	}
	message := result.Error.Error()
	if !strings.Contains(message, "command 参数缺失") || !strings.Contains(message, "unexpected end of JSON input") {
		t.Fatalf("expected parse-error guidance, got %q", message)
	}
}

func TestBashTool_CommandBatchCanStopOnError(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{failures: map[string]error{"first": fmt.Errorf("first failed")}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"stop_on_error": true,
		"parallel":      true,
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
	if result.Metadata["parallel_downgraded_reason"] != "stop_on_error_requires_ordered_execution" {
		t.Fatalf("expected graceful ordered fallback, got metadata=%#v", result.Metadata)
	}
}

func TestBashTool_CommandBatchAcceptsStringItems(t *testing.T) {
	tool := NewBashTool()
	inspector := &batchInspectExecuter{}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{"git diff --check", "go test ./internal/agent"},
	})

	if err != nil || !result.Success {
		t.Fatalf("expected string command batch to succeed, result=%#v err=%v", result, err)
	}
	if got := strings.Join(inspector.commands, "|"); got != "git diff --check|go test ./internal/agent" {
		t.Fatalf("unexpected commands: %q", got)
	}
}

func TestBashTool_CommandBatchRunsIndependentChecksInParallel(t *testing.T) {
	tool := NewBashTool()
	executer := &parallelBatchExecuter{started: make(chan string, 2), release: make(chan struct{})}
	tool.executer = executer
	type execution struct {
		result *toolkit.ToolResult
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"parallel": true, "max_parallel": 2,
			"commands": []interface{}{
				map[string]interface{}{"command": "first"},
				map[string]interface{}{"command": "second"},
			},
		})
		done <- execution{result: result, err: err}
	}()

	started := map[string]bool{}
	for len(started) < 2 {
		select {
		case command := <-executer.started:
			started[command] = true
		case <-time.After(time.Second):
			t.Fatal("expected both batch commands to start concurrently")
		}
	}
	close(executer.release)
	completed := <-done
	if completed.err != nil || !completed.result.Success {
		t.Fatalf("expected parallel batch to succeed, result=%#v err=%v", completed.result, completed.err)
	}
	if completed.result.Metadata["parallel"] != true || completed.result.Metadata["parallelism"] != 2 {
		t.Fatalf("expected parallel metadata, got %#v", completed.result.Metadata)
	}
	first := strings.Index(completed.result.Content, "first")
	second := strings.Index(completed.result.Content, "second")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("expected input-ordered output, got %q", completed.result.Content)
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

func TestBashTool_SearchShellUsesShorterInferredTimeout(t *testing.T) {
	t.Setenv(shellCommandTimeoutEnv, "")
	t.Setenv(shellCommandTimeoutMSEnv, "")
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: ""}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		// Pipeline keeps shell execution (simple pure rg is soft-redirected).
		"command": `rg -n "TodoItem" backend | Select-Object -First 20`,
	})
	if err != nil || !result.Success {
		t.Fatalf("expected search timeout inference success, result=%#v err=%v", result, err)
	}
	if inspector.lastTimeout != defaultSearchShellCommandTimeout {
		t.Fatalf("expected search timeout %v, got %v", defaultSearchShellCommandTimeout, inspector.lastTimeout)
	}
}

func TestBashTool_ExplicitTimeoutOverridesSearchInference(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "match"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "grep -R pattern .",
		"timeout": "45s",
	})
	if err != nil || !result.Success {
		t.Fatalf("expected explicit search timeout, result=%#v err=%v", result, err)
	}
	if inspector.lastTimeout != 45*time.Second {
		t.Fatalf("expected explicit 45s timeout, got %v", inspector.lastTimeout)
	}
}

func TestInferredShellCommandTimeout_SearchVsGoTest(t *testing.T) {
	if got := inferredShellCommandTimeout(`rg -n foo .`, 30*time.Second); got != defaultSearchShellCommandTimeout {
		t.Fatalf("rg inferred timeout=%v want %v", got, defaultSearchShellCommandTimeout)
	}
	if got := inferredShellCommandTimeout(`findstr /s /n TodoItem *.go`, defaultShellCommandTimeout); got != defaultSearchShellCommandTimeout {
		t.Fatalf("findstr inferred timeout=%v want %v", got, defaultSearchShellCommandTimeout)
	}
	// Env/default shorter than search clamp should be preserved.
	if got := inferredShellCommandTimeout(`rg pattern`, 5*time.Second); got != 5*time.Second {
		t.Fatalf("short fallback should win, got %v", got)
	}
	if got := inferredShellCommandTimeout(`go test ./...`, defaultShellCommandTimeout); got != defaultGoTestCommandTimeout {
		t.Fatalf("go test inferred timeout=%v want %v", got, defaultGoTestCommandTimeout)
	}
	if got := inferredShellCommandTimeout(`git status`, defaultShellCommandTimeout); got != defaultShellCommandTimeout {
		t.Fatalf("plain command inferred timeout=%v want %v", got, defaultShellCommandTimeout)
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

func TestBashTool_AcceptsPlainNumericTimeoutAsSeconds(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "git status",
		"timeout": "30",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 30*time.Second {
		t.Fatalf("expected numeric timeout to mean 30 seconds, got %v", inspector.lastTimeout)
	}
}

func TestBashTool_IgnoresZeroTimeoutPlaceholder(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "npx tsc --noEmit",
		"timeout_ms": 0,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected zero placeholder to use the default timeout, got error=%v", result.Error)
	}
	if inspector.lastTimeout != tool.timeout {
		t.Fatalf("expected default timeout %v, got %v", tool.timeout, inspector.lastTimeout)
	}
}

func TestBashTool_PrefersTimeoutSecWhenTimeoutMsIsSchemaNoise(t *testing.T) {
	// Live residual: models emit timeout_ms=1 with timeout_sec=30/60, which used
	// to hard-timeout after 1ms under strict timeout_ms priority.
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		// Use a pipeline so soft-redirect does not short-circuit timeout parsing.
		"command":     `rg -n "pattern" backend | Select-Object -First 20`,
		"timeout_ms":  1,
		"timeout_sec": 30,
		"timeout":     "",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 30*time.Second {
		t.Fatalf("expected timeout_sec=30 to win over noisy timeout_ms=1, got %v", inspector.lastTimeout)
	}
}

func TestBashTool_KeepsPlausibleShortTimeoutMsOverTimeoutSec(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":     "echo hi",
		"timeout_ms":  500,
		"timeout_sec": 30,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if inspector.lastTimeout != 500*time.Millisecond {
		t.Fatalf("expected plausible timeout_ms=500 to keep priority, got %v", inspector.lastTimeout)
	}
}

func TestParseShellCommandTimeout_NoiseReconciliation(t *testing.T) {
	got, err := parseShellCommandTimeout(map[string]interface{}{
		"timeout_ms":  1,
		"timeout_sec": 60,
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("parseShellCommandTimeout: %v", err)
	}
	if got != 60*time.Second {
		t.Fatalf("expected 60s from timeout_sec, got %v", got)
	}

	got, err = parseShellCommandTimeout(map[string]interface{}{
		"timeout_ms": 1,
		"timeout":    "45s",
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("parseShellCommandTimeout named: %v", err)
	}
	if got != 45*time.Second {
		t.Fatalf("expected 45s from timeout, got %v", got)
	}

	// Lone sub-floor numeric timeout_ms is treated as unit-confusion noise.
	got, err = parseShellCommandTimeout(map[string]interface{}{
		"timeout_ms": 30,
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("parseShellCommandTimeout lone ms: %v", err)
	}
	if got != 30*time.Second {
		t.Fatalf("expected lone timeout_ms=30 noise to use the default, got %v", got)
	}

	got, err = parseShellCommandTimeout(map[string]interface{}{
		"timeout": "30ms",
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("parseShellCommandTimeout explicit sub-floor duration: %v", err)
	}
	if got != 30*time.Millisecond {
		t.Fatalf("expected timeout=30ms to remain explicit, got %v", got)
	}
}

func TestBashTool_RejectsNegativeTimeout(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":    "npx tsc --noEmit",
		"timeout_ms": -1,
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

func TestBashTool_ExplicitOutputCapWinsOverDisablePlaceholder(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":            "echo hello",
		"output_bytes_cap":   8192,
		"disable_output_cap": true,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected bounded output cap to win, got error %#v", result.Error)
	}
	if !inspector.lastConfig.hasOutputBytesCap || inspector.lastConfig.outputBytesCap != 8192 {
		t.Fatalf("expected explicit output cap to be retained, got %+v", inspector.lastConfig)
	}
	if inspector.lastConfig.disableOutputCap {
		t.Fatalf("expected bounded cap to disable the unbounded option, got %+v", inspector.lastConfig)
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

func TestBashTool_IgnoresZeroStrictSchemaPlaceholders(t *testing.T) {
	tool := NewBashTool()
	inspector := &inspectExecuter{result: CommandExecutionResult{Output: "ok"}}
	tool.executer = inspector

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":          "git status",
		"commands":         []interface{}{},
		"output_bytes_cap": 0,
		"max_parallel":     0,
		"timeout_ms":       0,
		"timeout_sec":      0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected strict-schema placeholders to be ignored, got error: %v", result.Error)
	}
	if inspector.lastConfig.hasOutputBytesCap {
		t.Fatalf("did not expect zero output_bytes_cap to override the default: %+v", inspector.lastConfig)
	}
	if inspector.lastTimeout != tool.timeout {
		t.Fatalf("expected default timeout %v, got %v", tool.timeout, inspector.lastTimeout)
	}
}

func TestBashTool_BatchIgnoresZeroMaxParallelPlaceholder(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "ok"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": "git status"},
			map[string]interface{}{"command": "git diff --stat"},
		},
		"parallel":     true,
		"max_parallel": 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected zero max_parallel placeholder to use the default, got error: %v", result.Error)
	}
	if got := result.Metadata["parallelism"]; got != 2 {
		t.Fatalf("expected parallelism to be capped by the two commands, got %#v", got)
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

func TestFriendlyHintFor_RipgrepRegexParseError(t *testing.T) {
	hint := friendlyHintForSearchTool("rg", `rg -n "foo|\"bar\"" backend`, `rg: regex parse error:`, 2)
	if !strings.Contains(hint, "正则解析失败") {
		t.Fatalf("expected regex parse guidance, got %q", hint)
	}
	if !strings.Contains(hint, "grep") {
		t.Fatalf("expected grep tool recommendation, got %q", hint)
	}
	// Also through friendlyHintFor when output contains parse error text.
	hint = friendlyHintFor(
		`rg -n "foo" backend`,
		`rg: regex parse error: unclosed group`,
		fmt.Errorf("exit status 2"),
		"",
	)
	if !strings.Contains(hint, "正则解析失败") {
		t.Fatalf("expected regex parse guidance via friendlyHintFor, got %q", hint)
	}
}

func TestFriendlyHintFor_RipgrepExitOneNoMatches(t *testing.T) {
	hint := friendlyHintForSearchTool("rg", `rg -n "DoesNotExistSymbolXYZ" backend`, "", 1)
	if !strings.Contains(hint, "未匹配到结果") {
		t.Fatalf("expected no-match guidance, got %q", hint)
	}
	if !strings.Contains(hint, "grep") {
		t.Fatalf("expected grep tool recommendation, got %q", hint)
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

func TestBuildBashCommandFailureError_IncludesOutputSnippet(t *testing.T) {
	err := buildBashCommandFailureError(
		`go test ./internal/toolkit/tools -count=1`,
		"--- FAIL: TestFoo (0.01s)\n    foo_test.go:12: boom\nFAIL\n",
		fmt.Errorf("exit status 1"),
	)
	if err == nil {
		t.Fatal("expected enriched error")
	}
	message := err.Error()
	if !strings.Contains(message, "exit status 1") {
		t.Fatalf("expected original exit status, got %q", message)
	}
	if !strings.Contains(message, "go test ./internal/toolkit/tools") {
		t.Fatalf("expected command preview, got %q", message)
	}
	if !strings.Contains(message, "FAIL: TestFoo") {
		t.Fatalf("expected output snippet, got %q", message)
	}
}

func TestBuildBashCommandFailureError_EmptyOutputGuidance(t *testing.T) {
	err := buildBashCommandFailureError("false", "", fmt.Errorf("exit status 1"))
	if err == nil {
		t.Fatal("expected enriched error")
	}
	message := err.Error()
	if !strings.Contains(message, "无 stdout/stderr 输出") {
		t.Fatalf("expected empty-output guidance, got %q", message)
	}
}

func TestBashTool_NonZeroExitIsContentSuccess(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{
		result: CommandExecutionResult{Output: "cannot open file: no such file"},
		err:    fmt.Errorf("exit status 1"),
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "type missing.txt",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected non-zero exit to be content success, got error: %v", result.Error)
	}
	if result.Error != nil {
		t.Fatalf("expected nil Error on content success, got %v", result.Error)
	}
	if result.Metadata["exit_code"] != 1 {
		t.Fatalf("expected exit_code=1 metadata, got %#v", result.Metadata["exit_code"])
	}
	if result.Metadata["non_zero_exit"] != true {
		t.Fatalf("expected non_zero_exit metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "Exit code: 1") {
		t.Fatalf("expected Exit code header, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "cannot open file: no such file") {
		t.Fatalf("expected command output in content, got %q", result.Content)
	}
}

func TestBashTool_HardLaunchFailureStillFails(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{
		result: CommandExecutionResult{Output: ""},
		err:    fmt.Errorf("exec: \"pwsh\": executable file not found in %%PATH%%"),
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo hi",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected hard launch failure, got %#v", result)
	}
	if !strings.Contains(result.Error.Error(), "executable file not found") {
		t.Fatalf("expected launch error text, got %q", result.Error.Error())
	}
}

func TestExitCodeFromError_ParsesStatusStrings(t *testing.T) {
	if got := exitCodeFromError(fmt.Errorf("exit status 1")); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
	if got := exitCodeFromError(fmt.Errorf("命令执行失败: exit status 2\n提示: x")); got != 2 {
		t.Fatalf("expected 2 from wrapped message, got %d", got)
	}
	if got := exitCodeFromError(fmt.Errorf("exit status 0x80008083")); got != 0x80008083 {
		t.Fatalf("expected hex status, got %d", got)
	}
	if got := exitCodeFromError(fmt.Errorf("something else")); got != -1 {
		t.Fatalf("expected -1 for non-status error, got %d", got)
	}
}

func TestFriendlyHintFor_RipgrepExitOneFromWrappedError(t *testing.T) {
	// Models often see plain fmt.Errorf("exit status 1") after shell layers;
	// exitCodeFromError must still unlock the no-match guidance.
	hint := friendlyHintFor(
		`rg -n "DoesNotExistSymbolXYZ" backend`,
		"",
		fmt.Errorf("exit status 1"),
		"",
	)
	if !strings.Contains(hint, "未匹配到结果") {
		t.Fatalf("expected no-match guidance from wrapped exit status, got %q", hint)
	}
}

func TestIsSearchToolNoMatch(t *testing.T) {
	if !isSearchToolNoMatch(`rg -n "missing" backend`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("expected empty rg exit 1 to be no-match")
	}
	if !isSearchToolNoMatch(`grep -n missing backend`, "some noise without keywords", fmt.Errorf("exit status 1")) {
		t.Fatal("expected grep exit 1 without error keywords to be no-match")
	}
	if !isSearchToolNoMatch(`Select-String -Path backend/*.go -Pattern Missing`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("expected primary Select-String exit 1 without output to be no-match")
	}
	if isSearchToolNoMatch(`npx tsc --noEmit 2>&1 | Select-String -Pattern Missing`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("filter-only Select-String must not hide a primary build failure")
	}
	if isSearchToolNoMatch(`rg -n "x" backend`, "rg: regex parse error:", fmt.Errorf("exit status 1")) {
		t.Fatal("regex parse must not soft-succeed as no-match")
	}
	if isSearchToolNoMatch(`rg -n "x" backend`, "no such file or directory", fmt.Errorf("exit status 1")) {
		t.Fatal("path errors must not soft-succeed as no-match")
	}
	if isSearchToolNoMatch(`go test ./...`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("non-search commands must not soft-succeed")
	}
	timeoutErr := runtimeerrors.Wrap(runtimeerrors.ErrToolTimeout, "execution timed out", context.DeadlineExceeded)
	if isSearchToolNoMatch(`rg -n "x" backend`, "", timeoutErr) {
		t.Fatal("timeouts must not soft-succeed as no-match")
	}
}

func TestBashTool_SearchNoMatchSoftSucceeds(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{
		result: CommandExecutionResult{Output: ""},
		err:    fmt.Errorf("exit status 1"),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": `rg -n "DoesNotExistSymbolXYZ" backend | Select-Object -First 20`,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected soft success for rg no-match, got failure: %v", result.Error)
	}
	if result.Error != nil {
		t.Fatalf("expected nil Error on soft success, got %v", result.Error)
	}
	if result.Metadata["search_no_match"] != true {
		t.Fatalf("expected search_no_match metadata, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result metadata, got %#v", result.Metadata)
	}
	if result.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected empty outcome, got %#v", result.Metadata[toolresult.MetadataOutcomeKey])
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "grep") {
		t.Fatalf("expected next_action to prefer toolkit grep, got %q", next)
	}
	if !strings.Contains(result.Content, "未匹配到结果") {
		t.Fatalf("expected empty-evidence content, got %q", result.Content)
	}
}

func TestBashTool_SearchRealFailureIsContentSuccessWithGuidance(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{
		result: CommandExecutionResult{Output: "rg: regex parse error: unclosed group"},
		err:    fmt.Errorf("exit status 2"),
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": `rg -n "foo(" backend | Select-Object -First 20`,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected process exit content success for regex parse, got error: %v", result.Error)
	}
	if result.Metadata["exit_code"] != 2 {
		t.Fatalf("expected exit_code=2, got %#v", result.Metadata["exit_code"])
	}
	if !strings.Contains(result.Content, "Exit code: 2") {
		t.Fatalf("expected Exit code header, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "regex parse error") {
		t.Fatalf("expected regex parse body, got %q", result.Content)
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "grep") {
		t.Fatalf("expected next_action to prefer toolkit grep, got %q", next)
	}

	// Path/IO style exit 1 is also content success (process completed) with recovery guidance.
	tool.executer = fakeExecuter{
		result: CommandExecutionResult{Output: "rg: no such file or directory"},
		err:    fmt.Errorf("exit status 1"),
	}
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"command": `rg -n "x" missing-dir | Select-Object -First 20`,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected path exit to be content success, got error: %v content=%q", result.Error, result.Content)
	}
	if result.Metadata["exit_code"] != 1 {
		t.Fatalf("expected exit_code=1, got %#v", result.Metadata["exit_code"])
	}
	if !strings.Contains(result.Content, "no such file or directory") {
		t.Fatalf("expected path error body, got %q", result.Content)
	}
	next, _ = result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "grep") {
		t.Fatalf("expected search hard-failure next_action to prefer toolkit grep, got %q metadata=%#v", next, result.Metadata)
	}
}

func TestBuildBashCommandFailureError_TimeoutGuidance(t *testing.T) {
	timeoutErr := runtimeerrors.Wrap(runtimeerrors.ErrToolTimeout, "execution timed out after 2s", context.DeadlineExceeded)

	searchErr := buildBashCommandFailureError(`rg -n "TodoItem" backend`, "", timeoutErr)
	if searchErr == nil {
		t.Fatal("expected enriched timeout error")
	}
	if !runtimeerrors.Is(searchErr, runtimeerrors.ErrToolTimeout) {
		t.Fatalf("expected timeout type preserved via %%w, got %v", searchErr)
	}
	searchMsg := searchErr.Error()
	if !strings.Contains(searchMsg, "grep") || !strings.Contains(searchMsg, "rg -n") {
		t.Fatalf("expected search timeout guidance, got %q", searchMsg)
	}

	genericErr := buildBashCommandFailureError(`go test ./...`, "", timeoutErr)
	if genericErr == nil {
		t.Fatal("expected enriched generic timeout error")
	}
	if !runtimeerrors.Is(genericErr, runtimeerrors.ErrToolTimeout) {
		t.Fatalf("expected timeout type preserved, got %v", genericErr)
	}
	genericMsg := genericErr.Error()
	if !strings.Contains(genericMsg, "go test") {
		t.Fatalf("expected command in generic timeout, got %q", genericMsg)
	}
	if !strings.Contains(genericMsg, "缩小范围") && !strings.Contains(genericMsg, "timeout") {
		t.Fatalf("expected generic timeout recovery guidance, got %q", genericMsg)
	}
}

func TestBashTool_BatchOfSearchNoMatchSucceeds(t *testing.T) {
	tool := NewBashTool()
	// batchInspectExecuter returns Output + optional err; empty-ish output +
	// exit 1 on rg should soft-succeed per item and make the batch overall ok.
	inspector := &batchInspectExecuter{
		failures: map[string]error{
			`rg -n "AlphaMissing" .`: fmt.Errorf("exit status 1"),
			`rg -n "BetaMissing" .`:  fmt.Errorf("exit status 1"),
		},
	}
	// Override default "output for ..." content so soft-success path treats empty evidence.
	tool.executer = &searchNoMatchBatchExecuter{failures: inspector.failures}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"commands": []interface{}{
			map[string]interface{}{"command": `rg -n "AlphaMissing" .`},
			map[string]interface{}{"command": `rg -n "BetaMissing" .`},
		},
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected batch of only no-match searches to succeed, got error: %v content=%q", result.Error, result.Content)
	}
	if result.Metadata["failed_count"] != 0 {
		t.Fatalf("expected failed_count=0, got %#v", result.Metadata)
	}
}

// searchNoMatchBatchExecuter returns empty output + exit 1 for configured commands.
type searchNoMatchBatchExecuter struct {
	failures map[string]error
	commands []string
}

func (f *searchNoMatchBatchExecuter) Execute(_ context.Context, command string, _ time.Duration, _ ...ExecOption) (CommandExecutionResult, error) {
	f.commands = append(f.commands, command)
	return CommandExecutionResult{Output: ""}, f.failures[command]
}

func TestParseBashCommandBatch_CoercesJSONStringArray(t *testing.T) {
	items, batch, err := parseBashCommandBatch(map[string]interface{}{
		"commands": `[{"command":"go test ./..."},{"command":"git status"}]`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !batch {
		t.Fatal("expected batch mode")
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 commands, got %#v", items)
	}
	if items[0].Command != "go test ./..." || items[1].Command != "git status" {
		t.Fatalf("unexpected coerced commands: %#v", items)
	}
}

func TestParseBashCommandBatch_CoercesBareCommandString(t *testing.T) {
	items, batch, err := parseBashCommandBatch(map[string]interface{}{
		"commands": "go test ./internal/prompt/ -count=1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !batch || len(items) != 1 || items[0].Command != "go test ./internal/prompt/ -count=1" {
		t.Fatalf("expected single-item batch, got batch=%v items=%#v", batch, items)
	}
}

func TestBashTool_WindowsHeredocPreflight(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("heredoc preflight is Windows-specific")
	}
	tool := NewBashTool()
	// Ensure we never reach the executer for blocked dialect.
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "should-not-run"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "python - <<'PY'\nprint(1)\nPY",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected heredoc preflight failure, got %#v", result)
	}
	if !strings.Contains(result.Error.Error(), "heredoc") {
		t.Fatalf("expected heredoc guidance, got %q", result.Error.Error())
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(strings.ToLower(next), "write") && !strings.Contains(strings.ToLower(next), "python -c") {
		t.Fatalf("expected next_action to steer away from heredoc, got %q", next)
	}
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code != "TOOL_SHELL_COMPAT" {
		t.Fatalf("expected TOOL_SHELL_COMPAT error_code for heredoc preflight, got %#v", result.Metadata)
	}
	if retryable, _ := result.Metadata[toolresult.MetadataRetryableKey].(bool); retryable {
		t.Fatalf("shell preflight must not be retryable, got %#v", result.Metadata)
	}
}

func TestLooksLikeBashHeredoc(t *testing.T) {
	if !looksLikeBashHeredoc("cat <<EOF\nhello\nEOF") {
		t.Fatal("expected classic heredoc to match")
	}
	if !looksLikeBashHeredoc("python - <<'PY'\nprint(1)\nPY") {
		t.Fatal("expected quoted heredoc to match")
	}
	if looksLikeBashHeredoc("git status 2>&1") {
		t.Fatal("PowerShell/stream redirect must not look like heredoc")
	}
	if looksLikeBashHeredoc("if ($a -lt 1) { }") {
		t.Fatal("PowerShell comparison must not look like heredoc")
	}
}

func TestBashCommandFailureNextAction_SearchRegex(t *testing.T) {
	next := bashCommandFailureNextAction(
		`rg -n "foo(" backend`,
		"rg: regex parse error:",
		fmt.Errorf("exit status 2"),
	)
	if !strings.Contains(next, "grep") {
		t.Fatalf("expected grep recovery next_action, got %q", next)
	}
}

func TestIsSearchToolNoMatch_IgnoresPowerShellNoise(t *testing.T) {
	// PowerShell NativeCommandError chrome contains the substring "error" but
	// still represents rg no-match under exit status 1.
	noise := "\x1b[31;1mNativeCommandError:\x1b[0m\n+ CategoryInfo          : NotSpecified\n+ FullyQualifiedErrorId : NativeCommandError\n---\n"
	if !isSearchToolNoMatch(`rg -n "MissingSymbol" backend`, noise, fmt.Errorf("exit status 1")) {
		t.Fatal("expected PowerShell noise + empty search body to soft-succeed as no-match")
	}
	if !isSearchToolNoMatch(`rg -n "Missing" . | Select-Object -First 20`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("expected piped rg no-match to soft-succeed")
	}
	// Real regex failures must still hard-fail even with noise mixed in.
	if isSearchToolNoMatch(`rg -n "foo(" backend`, noise+"\nrg: regex parse error:\nunclosed group", fmt.Errorf("exit status 1")) {
		t.Fatal("regex parse must not soft-succeed even with PowerShell noise")
	}
}

func TestBashTool_ShellToolkitCommandPreflight(t *testing.T) {
	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "view -path internal/dbmigration/sql_migrations.go -offset 1040 -limit 50",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected preflight block for shell-invoked view, got %#v", result)
	}
	if !strings.Contains(result.Error.Error(), "toolkit") && !strings.Contains(result.Error.Error(), "view") {
		t.Fatalf("expected toolkit misuse guidance, got %q", result.Error.Error())
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "view") {
		t.Fatalf("expected next_action to prefer toolkit view, got %q", next)
	}
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code != "TOOL_SHELL_COMPAT" {
		t.Fatalf("expected TOOL_SHELL_COMPAT for toolkit misuse preflight, got %#v", result.Metadata)
	}

	// Real system grep without toolkit-style flags should not be preflight-blocked.
	if name, blocked := looksLikeShellInvokedToolkitCommand(`grep -n "foo" backend`); blocked {
		t.Fatalf("did not expect system grep to be blocked, got name=%q", name)
	}
	// Toolkit-style grep schema should be blocked.
	if name, blocked := looksLikeShellInvokedToolkitCommand(`grep -pattern foo -path backend`); !blocked || name != "grep" {
		t.Fatalf("expected toolkit-style grep to be blocked, got name=%q blocked=%v", name, blocked)
	}
	// Ordinary shell ls without toolkit flags remains allowed.
	if _, blocked := looksLikeShellInvokedToolkitCommand("ls"); blocked {
		t.Fatal("bare ls should remain a shell command")
	}
}

func TestLooksLikeSearchPathShellGlob(t *testing.T) {
	// Residual Windows failure: globs in path position.
	if !looksLikeSearchPathShellGlob(`rg -n "ErrToolTimeout|func Is\(" backend/internal/errors/*.go`) {
		t.Fatal("expected path-position *.go to be detected")
	}
	if !looksLikeSearchPathShellGlob(`rg -n foo backend/internal/toolkit/tools/*.go | Select-Object -First 20`) {
		t.Fatal("expected piped path glob to be detected")
	}
	if !looksLikeSearchPathShellGlob(`rg -n pattern backend/**/*.go`) {
		t.Fatal("expected **/*.go path glob to be detected")
	}
	// -g / --glob values are legitimate and must not preflight-block.
	if looksLikeSearchPathShellGlob(`rg -n foo -g "*.go" backend`) {
		t.Fatal("rg -g \"*.go\" path should not look like path shell glob")
	}
	if looksLikeSearchPathShellGlob(`rg -n foo --glob=*.go backend`) {
		t.Fatal("rg --glob=*.go should not look like path shell glob")
	}
	if looksLikeSearchPathShellGlob(`rg -n "foo*" backend`) {
		t.Fatal("glob metacharacters only in pattern must not be treated as path glob")
	}
	if looksLikeSearchPathShellGlob(`go test ./...`) {
		t.Fatal("non-search commands must not match")
	}
}

func TestBashTool_SearchPathShellGlobPreflight(t *testing.T) {
	tool := NewBashTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": `rg -n "func toolResult" backend/internal/toolkit/tools/*.go`,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected preflight block for path shell glob, got %#v", result)
	}
	errText := result.Error.Error()
	if !strings.Contains(errText, "通配符") && !strings.Contains(errText, "glob") && !strings.Contains(errText, "-g") {
		t.Fatalf("expected path-glob recovery message, got %q", errText)
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(strings.ToLower(next), "grep") && !strings.Contains(next, "-g") {
		t.Fatalf("expected next_action to steer to grep/-g, got %q", next)
	}
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code != "TOOL_SHELL_COMPAT" {
		t.Fatalf("expected TOOL_SHELL_COMPAT for path-glob preflight, got %#v", result.Metadata)
	}
}

func TestLooksLikeSimpleShellCodeSearch(t *testing.T) {
	if tool, ok := looksLikeSimpleShellCodeSearch(`rg -n "foo" backend`); !ok || tool != "rg" {
		t.Fatalf("expected simple rg code search, got tool=%q ok=%v", tool, ok)
	}
	if _, ok := looksLikeSimpleShellCodeSearch(`rg -n "foo" backend | Select-Object -First 20`); ok {
		t.Fatal("pipelines must not soft-redirect")
	}
	if _, ok := looksLikeSimpleShellCodeSearch(`rg --files -g "*.go"`); ok {
		t.Fatal("rg --files must remain executable")
	}
	if _, ok := looksLikeSimpleShellCodeSearch(`grep -n foo backend`); ok {
		t.Fatal("system grep is not soft-redirected")
	}
}

func TestBashTool_SimpleShellCodeSearchSoftRedirect(t *testing.T) {
	tool := NewBashTool()
	tool.executer = fakeExecuter{result: CommandExecutionResult{Output: "should-not-run"}}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": `rg -n "DoesNotExistSymbolXYZ" backend`,
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected soft redirect success, got error: %v", result.Error)
	}
	if result.Metadata["shell_search_redirected"] != true {
		t.Fatalf("expected shell_search_redirected metadata, got %#v", result.Metadata)
	}
	if result.Metadata["executed"] != false {
		t.Fatalf("expected executed=false, got %#v", result.Metadata["executed"])
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "grep") {
		t.Fatalf("expected next_action to prefer toolkit grep, got %q", next)
	}
	if !strings.Contains(result.Content, "软重定向") && !strings.Contains(result.Content, "toolkit") {
		t.Fatalf("expected redirect content, got %q", result.Content)
	}
}

func TestClassifyHardShellExecutionErrorCode(t *testing.T) {
	if got := classifyHardShellExecutionErrorCode(context.DeadlineExceeded, ""); got != "TOOL_TIMEOUT" {
		t.Fatalf("deadline => TOOL_TIMEOUT, got %q", got)
	}
	if got := classifyHardShellExecutionErrorCode(context.Canceled, ""); got != "AGENT_RUN_CANCELED" {
		t.Fatalf("canceled => AGENT_RUN_CANCELED, got %q", got)
	}
	if got := classifyHardShellExecutionErrorCode(fmt.Errorf("%s", `exec: "head": executable file not found in %PATH%`), ""); got != "TOOL_SHELL_COMPAT" {
		t.Fatalf("missing executable => TOOL_SHELL_COMPAT, got %q", got)
	}
	if got := classifyHardShellExecutionErrorCode(fmt.Errorf("permission denied"), ""); got != "AGENT_PERMISSION" {
		t.Fatalf("permission => AGENT_PERMISSION, got %q", got)
	}
	// Finished non-zero exits never call this helper; still ensure generic launch
	// failures do not collapse to empty.
	if got := classifyHardShellExecutionErrorCode(fmt.Errorf("spawn failed mysteriously"), ""); got != "TOOL_EXECUTION" {
		t.Fatalf("unknown hard fail => TOOL_EXECUTION, got %q", got)
	}
}

func TestIsSearchToolNoMatch_PathIOErrorHardFails(t *testing.T) {
	ioBody := `rg: backend/internal/errors/*.go: IO error for operation on backend/internal/errors/*.go: 文件名、目录名或卷标语法不正确。 (os error 123)`
	if isSearchToolNoMatch(`rg -n "x" backend/internal/errors/*.go`, ioBody, fmt.Errorf("exit status 1")) {
		t.Fatal("path IO error must not soft-succeed as no-match")
	}
	if !searchOutputLooksLikePathIOError(ioBody) {
		t.Fatal("expected path IO classifier to match Windows os error 123 body")
	}
	// Empty no-match still soft-succeeds.
	if !isSearchToolNoMatch(`rg -n "Missing" backend`, "", fmt.Errorf("exit status 1")) {
		t.Fatal("empty no-match should still soft-succeed")
	}
}

func TestBashCommandFailureNextAction_PathGlob(t *testing.T) {
	next := bashCommandFailureNextAction(
		`rg -n foo backend/**/*.go`,
		`rg: backend/**/*.go: IO error ... os error 123`,
		fmt.Errorf("exit status 1"),
	)
	if !strings.Contains(strings.ToLower(next), "glob") && !strings.Contains(next, "-g") {
		t.Fatalf("expected path-glob next_action, got %q", next)
	}
}

func TestBashCommandFailureNextAction_GitIgnoredPath(t *testing.T) {
	next := bashCommandFailureNextAction(
		`git add secrets/token.env`,
		"The following paths are ignored by one of your .gitignore files:\nsecrets/token.env\nhint: Use -f if you really want to add them.",
		fmt.Errorf("exit status 1"),
	)
	if !strings.Contains(next, "git check-ignore") {
		t.Fatalf("expected check-ignore guidance, got %q", next)
	}
	if !strings.Contains(next, "git add -f") {
		t.Fatalf("expected intentional force-add guidance, got %q", next)
	}
	if !strings.Contains(strings.ToLower(next), "do not retry") {
		t.Fatalf("expected no unchanged-retry guidance, got %q", next)
	}
}

func TestLooksLikeGitIgnoredPathFailure(t *testing.T) {
	if !looksLikeGitIgnoredPathFailure(
		"git add ignored.txt",
		strings.ToLower("The following paths are ignored by one of your .gitignore files:\nignored.txt"),
	) {
		t.Fatal("expected gitignore failure classifier to match")
	}
	if looksLikeGitIgnoredPathFailure("rg ignored", "the following paths are ignored by one of your .gitignore files") {
		t.Fatal("non-git commands must not match gitignore classifier")
	}
	if looksLikeGitIgnoredPathFailure("git status", "nothing to commit, working tree clean") {
		t.Fatal("clean git status must not look like ignore failure")
	}
}
