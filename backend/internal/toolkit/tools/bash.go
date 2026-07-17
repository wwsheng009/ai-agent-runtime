package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// BashTool Bash 命令执行工具
type BashTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	executer  CommandExecuter
	timeout   time.Duration
	blacklist []string
}

// 危险命令黑名单
var defaultBlacklist = []string{
	"rm -rf /",
	"rm -rf /*",
	"mkfs",
	"dd if=/dev/zero",
	"dd if=/dev/urandom",
	":(){ :|:& };:",
	"chmod -R 777 /",
	"chown -R",
	"> /dev/sda",
	"> /dev/hda",
}

const (
	modelHistoryArtifactThresholdBytes = 12 * 1024
	defaultShellCommandTimeout         = 30 * time.Second
	defaultGoTestCommandTimeout        = 5 * time.Minute
	shellCommandTimeoutEnv             = "AICLI_SHELL_COMMAND_TIMEOUT"
	shellCommandTimeoutMSEnv           = "AICLI_SHELL_COMMAND_TIMEOUT_MS"
)

// NewBashTool 创建 Bash 工具
func NewBashTool() *BashTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "单条 Shell 命令。需要顺序执行多条独立检查时改用 commands，减少 LLM 往返。用 workdir 切换目录；Windows PowerShell 没有 head 时使用 Select-Object。",
			},
			"commands": map[string]interface{}{
				"type":        "array",
				"description": "命令批次。默认顺序执行；仅当各命令互不依赖且只读时可设置 parallel=true。每项可覆盖 workdir 和 timeout。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command":     map[string]interface{}{"type": "string"},
						"workdir":     map[string]interface{}{"type": "string"},
						"timeout":     map[string]interface{}{"type": "string"},
						"timeout_ms":  map[string]interface{}{"type": "integer", "minimum": 1},
						"timeout_sec": map[string]interface{}{"type": "integer", "minimum": 1},
					},
					"required":             []string{"command"},
					"additionalProperties": false,
				},
			},
			"stop_on_error": map[string]interface{}{
				"type":        "boolean",
				"description": "commands 批次中某条失败后是否停止；默认 false，以便一次收集全部检查结果。",
			},
			"parallel": map[string]interface{}{
				"type":        "boolean",
				"description": "commands 是否并发执行。仅用于互不依赖的只读检查；默认 false。结果仍按输入顺序返回。",
			},
			"max_parallel": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "parallel=true 时的最大并发命令数。默认根据 CPU 自动选择，最多 4；显式设置可覆盖。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。",
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令超时，例如 30s、2m、5m。普通命令默认 30s，go test 未显式设置时自动使用至少 5m；环境变量和显式参数仍可覆盖。",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时毫秒数，必须为正整数。优先级高于 timeout 和 timeout_sec。",
			},
			"timeout_sec": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时秒数，必须为正整数。优先级低于 timeout_ms，高于 timeout。",
			},
			"output_bytes_cap": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：stdout/stderr 合并输出的保留上限（字节）。用于覆盖默认 256KB capture limit；必须为正整数。若同时设置 disable_output_cap=true，为保证资源边界，以本参数为准。",
			},
			"disable_output_cap": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时关闭 shell 输出 capture limit，尽量保留完整原始输出；若同时设置 output_bytes_cap，则保留后者的显式上限。",
			},
			"mutated_paths": map[string]interface{}{
				"type":        "array",
				"description": "可选：命令将修改的文件路径列表，用于变更追踪与回滚。",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"required": []string{},
	}

	return &BashTool{
		BaseTool: toolkit.NewBaseTool(
			"bash",
			"执行一条 Shell 命令，或用 commands 顺序执行一批检查并一次返回全部结果。仅当后续命令依赖前一条输出且需模型决策时才拆分。Windows 默认使用 PowerShell；没有 head 时使用 Select-Object。",
			"1.2.0",
			parameters,
			true, // 支持直接调用
		),
		executer:  &DefaultCommandExecuter{},
		timeout:   resolveDefaultShellCommandTimeout(),
		blacklist: defaultBlacklist,
	}
}

func (b *BashTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: false,
	}
}

type CommandExecutionResult struct {
	Output                 string
	Truncated              bool
	TotalBytes             int
	TotalLines             int
	RetainedBytes          int
	OmittedBytes           int
	CaptureLimitBytes      int
	CaptureLimitDisabled   bool
	RawOutputArtifactPath  string
	RawOutputArtifactError string
	ShellType              string
	ShellPath              string
	TimeoutMs              int64
	TimeoutRequestedMs     int64
	TimeoutEffectiveMs     int64
	TimeoutSource          string
}

type outputCaptureSettings struct {
	outputBytesCap    int
	hasOutputBytesCap bool
	disableOutputCap  bool
}

type bashCommandBatchItem struct {
	Command string
	Params  map[string]interface{}
}

// Execute 实现 Tool 接口
func (b *BashTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	commands, batchRequested, err := parseBashCommandBatch(params)
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}, nil
	}
	if batchRequested {
		return b.executeBatch(ctx, params, commands)
	}
	command, ok := params["command"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("command 参数缺失或类型错误"),
		}, nil
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("command 参数为空"),
		}, nil
	}
	mutatedPaths := extractStringList(params["mutated_paths"])
	workdir := extractString(params["workdir"])
	captureSettings, err := parseOutputCaptureSettings(params)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	defaultTimeout := b.timeout
	if !hasExplicitShellTimeout(params) {
		defaultTimeout = inferredShellCommandTimeout(command, defaultTimeout)
	}
	timeout, err := parseShellCommandTimeout(params, defaultTimeout)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	timeoutSource := runtimeexecution.TimeoutSourceToolDefault
	if hasExplicitShellTimeout(params) {
		timeoutSource = runtimeexecution.TimeoutSourceToolArgument
	}
	ctx = runtimeexecution.WithTimeoutRequestSource(ctx, timeoutSource)

	// 检查黑名单
	if b.isBlacklisted(command) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("命令被禁止执行（安全限制）"),
		}, nil
	}

	// 使用 executer 执行命令
	execResult, err := b.executeCommand(ctx, command, workdir, timeout, captureSettings)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Content:    execResult.Output,
			Metadata:   buildCommandExecutionMetadata(command, mutatedPaths, execResult),
			Error:      err,
		}, nil
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    execResult.Output,
		Metadata:   buildCommandExecutionMetadata(command, mutatedPaths, execResult),
	}, nil
}

func parseBashCommandBatch(params map[string]interface{}) ([]bashCommandBatchItem, bool, error) {
	raw, exists := params["commands"]
	if !exists || raw == nil {
		return nil, false, nil
	}
	values, ok := raw.([]interface{})
	if !ok {
		if typed, typedOK := raw.([]map[string]interface{}); typedOK {
			values = make([]interface{}, 0, len(typed))
			for _, item := range typed {
				values = append(values, item)
			}
		} else {
			return nil, true, fmt.Errorf("commands 参数必须是对象数组")
		}
	}
	if len(values) == 0 {
		// Strict tool schemas make optional nullable fields required at the
		// provider boundary. Some models materialize an unused commands field as
		// [] even when they supplied a valid single command. Treat that shape as
		// the single-command form instead of spending another model round trip.
		if extractString(params["command"]) != "" {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("commands 参数不能为空")
	}
	commands := make([]bashCommandBatchItem, 0, len(values))
	for index, rawItem := range values {
		if command, isString := rawItem.(string); isString {
			command = strings.TrimSpace(command)
			if command == "" {
				return nil, true, fmt.Errorf("commands[%d] 不能为空", index)
			}
			commands = append(commands, bashCommandBatchItem{Command: command, Params: map[string]interface{}{}})
			continue
		}
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			return nil, true, fmt.Errorf("commands[%d] 必须是对象", index)
		}
		command := strings.TrimSpace(extractString(item["command"]))
		if command == "" {
			return nil, true, fmt.Errorf("commands[%d].command 参数缺失或为空", index)
		}
		commands = append(commands, bashCommandBatchItem{Command: command, Params: item})
	}
	return commands, true, nil
}

func (b *BashTool) executeBatch(ctx context.Context, parent map[string]interface{}, commands []bashCommandBatchItem) (*toolkit.ToolResult, error) {
	stopOnError, _ := resolveBoolParam(parent, "stop_on_error")
	parallel, _ := resolveBoolParam(parent, "parallel")
	if parallel && stopOnError {
		result, err := b.executeSequentialBatch(ctx, parent, commands, true)
		if result != nil && result.Metadata != nil {
			result.Metadata["parallel_requested"] = true
			result.Metadata["parallel_downgraded_reason"] = "stop_on_error_requires_ordered_execution"
		}
		return result, err
	}
	if parallel {
		return b.executeParallelBatch(ctx, parent, commands)
	}
	return b.executeSequentialBatch(ctx, parent, commands, stopOnError)
}

func (b *BashTool) executeSequentialBatch(ctx context.Context, parent map[string]interface{}, commands []bashCommandBatchItem, stopOnError bool) (*toolkit.ToolResult, error) {
	results := make([]*toolkit.ToolResult, 0, len(commands))
	for _, item := range commands {
		result := b.executeBatchItem(ctx, parent, item)
		results = append(results, result)
		if stopOnError && !result.Success {
			break
		}
	}
	return buildBashBatchResult(ctx, parent, commands, results, stopOnError, 1)
}

func (b *BashTool) executeParallelBatch(ctx context.Context, parent map[string]interface{}, commands []bashCommandBatchItem) (*toolkit.ToolResult, error) {
	parallelism, err := resolveBashBatchParallelism(parent, len(commands))
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}, nil
	}
	results := make([]*toolkit.ToolResult, len(commands))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for index, item := range commands {
		wg.Add(1)
		go func(index int, item bashCommandBatchItem) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[index] = &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: ctx.Err()}
				return
			}
			results[index] = b.executeBatchItem(ctx, parent, item)
		}(index, item)
	}
	wg.Wait()
	return buildBashBatchResult(ctx, parent, commands, results, false, parallelism)
}

func resolveBashBatchParallelism(params map[string]interface{}, commandCount int) (int, error) {
	if commandCount <= 1 {
		return 1, nil
	}
	parallelism := runtime.GOMAXPROCS(0)
	if parallelism <= 0 {
		parallelism = 1
	}
	if parallelism > 4 {
		parallelism = 4
	}
	if raw, ok := params["max_parallel"]; ok && raw != nil {
		if !isNumericZero(raw) {
			value, err := extractPositiveInt(raw)
			if err != nil {
				return 0, fmt.Errorf("max_parallel 参数无效: %w", err)
			}
			parallelism = value
		}
	}
	if parallelism > commandCount {
		parallelism = commandCount
	}
	return parallelism, nil
}

func (b *BashTool) executeBatchItem(ctx context.Context, parent map[string]interface{}, item bashCommandBatchItem) *toolkit.ToolResult {
	commandParams := bashBatchCommandParams(parent, item.Params)
	commandParams["command"] = item.Command
	delete(commandParams, "commands")
	delete(commandParams, "parallel")
	delete(commandParams, "max_parallel")
	delete(commandParams, "stop_on_error")
	result, err := b.Execute(ctx, commandParams)
	if err != nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: err}
	}
	if result == nil {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: fmt.Errorf("bash command returned no result")}
	}
	return result
}

func buildBashBatchResult(ctx context.Context, parent map[string]interface{}, commands []bashCommandBatchItem, results []*toolkit.ToolResult, stopOnError bool, parallelism int) (*toolkit.ToolResult, error) {
	sections := make([]string, 0, len(commands))
	items := make([]map[string]interface{}, 0, len(commands))
	failed := 0
	commandTexts := make([]string, 0, len(commands))
	artifactPaths := make([]string, 0, len(commands))
	batchArtifactPath := ""
	for index, result := range results {
		item := commands[index]
		commandTexts = append(commandTexts, item.Command)
		entry := map[string]interface{}{
			"index": index, "command": item.Command, "success": result != nil && result.Success,
		}
		status := "ok"
		content := ""
		if result != nil {
			content = result.Content
			entry["metadata"] = result.Metadata
			if artifactPath := extractString(result.Metadata["raw_output_artifact_path"]); artifactPath != "" {
				artifactPaths = append(artifactPaths, artifactPath)
			}
		}
		if result == nil || !result.Success {
			failed++
			status = "failed"
			if result != nil && result.Error != nil {
				entry["error"] = result.Error.Error()
				if strings.TrimSpace(content) == "" {
					content = result.Error.Error()
				}
			}
		}
		sections = append(sections, fmt.Sprintf("===== command %d/%d [%s] =====\n%s\n%s", index+1, len(commands), status, item.Command, strings.TrimSpace(content)))
		items = append(items, entry)
	}
	metadata := map[string]interface{}{
		"batch": true, "requested_count": len(commands), "executed_count": len(items),
		"failed_count": failed, "stop_on_error": stopOnError, "items": items,
		"parallel": parallelism > 1, "parallelism": parallelism,
		"command": strings.Join(commandTexts, "\n"), "commands": commandTexts,
	}
	batchOutput := strings.Join(sections, "\n\n")
	if mutatedPaths := extractStringList(parent["mutated_paths"]); len(mutatedPaths) > 0 {
		metadata["mutated_paths"] = mutatedPaths
	}
	if len(batchOutput) > modelHistoryArtifactThresholdBytes {
		artifactPath, artifactErr := runtimeexecutor.PersistShellOutputArtifact(
			"toolkit", "bash command batch", toolctx.ShellOutputArtifactDir(ctx), batchOutput,
		)
		if artifactErr != nil {
			metadata["raw_output_artifact_error"] = artifactErr.Error()
		} else if strings.TrimSpace(artifactPath) != "" {
			artifactPaths = append(artifactPaths, artifactPath)
			batchArtifactPath = artifactPath
		}
	}
	if len(artifactPaths) > 0 {
		metadata["raw_output_artifact_paths"] = artifactPaths
		if batchArtifactPath == "" {
			batchArtifactPath = artifactPaths[0]
		}
		metadata["raw_output_artifact_path"] = batchArtifactPath
	}
	if failed > 0 {
		return &toolkit.ToolResult{
			Success: false, OutputKind: toolresult.KindText, Content: batchOutput,
			Metadata: metadata, Error: fmt.Errorf("bash command batch completed with %d failure(s)", failed),
		}, nil
	}
	return &toolkit.ToolResult{Success: true, OutputKind: toolresult.KindText, Content: batchOutput, Metadata: metadata}, nil
}

func bashBatchCommandParams(parent, item map[string]interface{}) map[string]interface{} {
	params := make(map[string]interface{}, len(parent)+len(item))
	for key, value := range parent {
		params[key] = value
	}
	for key, value := range item {
		params[key] = value
	}
	return params
}

func (b *BashTool) executeCommand(ctx context.Context, command string, workdir string, timeout time.Duration, captureSettings outputCaptureSettings) (CommandExecutionResult, error) {
	// 解析工作目录
	resolvedWorkdir, err := resolveWorkdir(workdir)
	if err != nil {
		return CommandExecutionResult{}, err
	}
	budget := runtimeexecution.ResolveTimeout(ctx, timeout)

	if b.sandbox == nil {
		opts := []ExecOption{WithWorkdir(resolvedWorkdir)}
		if captureSettings.hasOutputBytesCap {
			opts = append(opts, WithOutputBytesCap(captureSettings.outputBytesCap))
		}
		if captureSettings.disableOutputCap {
			opts = append(opts, WithDisableOutputCap())
		}
		result, err := b.executer.Execute(ctx, command, timeout, opts...)
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}

	mainCmd := extractPrimaryCommand(command)
	if err := b.sandbox.ValidateCommand(mainCmd); err != nil {
		return CommandExecutionResult{}, wrapSandboxPermissionError("sandbox denied command execution", err, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
		})
	}

	if err := b.sandbox.CheckPermission(runtimeexecutor.OpExecute, resolvedWorkdir); err != nil {
		return CommandExecutionResult{}, wrapSandboxPermissionError("sandbox denied command working directory", err, map[string]interface{}{
			"policy":      "sandbox",
			"operation":   string(runtimeexecutor.OpExecute),
			"target_path": resolvedWorkdir,
			"command":     mainCmd,
		})
	}

	if configured := b.sandbox.Config().MaxExecutionTime; configured > 0 {
		budget = runtimeexecution.LimitTimeout(budget, configured, runtimeexecution.TimeoutSourceSandboxPolicy)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, budget.Effective)
	defer cancel()

	// 使用智能 shell 检测
	shell := runtimeexecutor.DefaultUserShell()
	shellCmd := shell.DeriveExecArgs(command, false)

	if err := b.sandbox.CheckCommandDenied(shellCmd[0]); err != nil {
		return CommandExecutionResult{}, wrapSandboxPermissionError("sandbox denied shell launcher", err, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
			"launcher":  shellCmd[0],
		})
	}

	cmd := exec.CommandContext(cmdCtx, shellCmd[0], shellCmd[1:]...)
	cmd.Dir = resolvedWorkdir
	cmd.Env = runtimeexecutor.BuildFilteredEnv(b.sandbox, os.Environ())

	// PowerShell 需要 UTF-8 输出编码
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8(cmd)
	}

	outputMirror := runtimeexecutor.OutputMirrorFromContext(ctx)
	artifactRoot := toolctx.ShellOutputArtifactDir(ctx)
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifactAndMirror(cmd, captureSettings.captureLimitBytes(), "toolkit", command, artifactRoot, outputMirror)
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command, artifactRoot)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.TimeoutError(budget)
		}
		if cmdCtx.Err() == context.Canceled {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.ContextCancellationError(ctx)
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		result = applyCommandExecutionShell(result, shell)
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}
	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	result = applyCommandExecutionShell(result, shell)
	applyCommandTimeoutBudget(&result, budget)
	return result, nil
}

func (b *BashTool) isBlacklisted(command string) bool {
	cmdLower := strings.ToLower(command)
	for _, blocked := range b.blacklist {
		if strings.Contains(cmdLower, strings.ToLower(blocked)) {
			return true
		}
	}
	return false
}

// --- CommandExecuter interface (updated with options) ---

// CommandExecuter 命令执行器接口
type CommandExecuter interface {
	Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error)
}

// ExecOption configures command execution.
type ExecOption func(*execConfig)

type execConfig struct {
	workdir           string
	outputBytesCap    int
	hasOutputBytesCap bool
	disableOutputCap  bool
}

// WithWorkdir sets the working directory for command execution.
func WithWorkdir(dir string) ExecOption {
	return func(c *execConfig) {
		c.workdir = dir
	}
}

// WithOutputBytesCap overrides the retained output cap for shell capture.
func WithOutputBytesCap(bytes int) ExecOption {
	return func(c *execConfig) {
		c.outputBytesCap = bytes
		c.hasOutputBytesCap = true
	}
}

// WithDisableOutputCap disables shell output capture truncation.
func WithDisableOutputCap() ExecOption {
	return func(c *execConfig) {
		c.disableOutputCap = true
	}
}

// DefaultCommandExecuter 默认命令执行器
type DefaultCommandExecuter struct{}

// Execute 实现命令执行
func (e *DefaultCommandExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (CommandExecutionResult, error) {
	cfg := &execConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	budget := runtimeexecution.ResolveTimeout(ctx, timeout)
	cmdCtx, cancel := context.WithTimeout(ctx, budget.Effective)
	defer cancel()

	// 使用智能 shell 检测
	shell := runtimeexecutor.DefaultUserShell()
	shellArgs := shell.DeriveExecArgs(command, false)

	cmd := exec.CommandContext(cmdCtx, shellArgs[0], shellArgs[1:]...)

	// 设置工作目录
	if cfg.workdir != "" {
		cmd.Dir = cfg.workdir
	}

	// 过滤敏感环境变量
	cmd.Env = runtimeexecutor.FilterSensitiveEnv(os.Environ())

	// PowerShell 需要 UTF-8 输出编码
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8(cmd)
	}

	outputMirror := runtimeexecutor.OutputMirrorFromContext(ctx)
	if outputMirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}

	artifactRoot := toolctx.ShellOutputArtifactDir(ctx)
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifactAndMirror(cmd, captureLimitBytesFromExecConfig(cfg), "toolkit", command, artifactRoot, outputMirror)
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command, artifactRoot)

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.TimeoutError(budget)
		}
		if cmdCtx.Err() == context.Canceled {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			result = applyCommandExecutionShell(result, shell)
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.ContextCancellationError(ctx)
		}

		// 检查常见错误并给出友好提示
		friendlyHint := friendlyHintFor(command, capture.Output, err, cfg.workdir)
		if friendlyHint != "" {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			applyCommandTimeoutBudget(&result, budget)
			return result, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, GetShellEnvironmentInfo())
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		result = applyCommandExecutionShell(result, shell)
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}

	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	result = applyCommandExecutionShell(result, shell)
	applyCommandTimeoutBudget(&result, budget)
	return result, nil
}

// --- Helper functions ---

func commandExecutionFromCapture(capture runtimeexecutor.CombinedOutputCapture) CommandExecutionResult {
	return CommandExecutionResult{
		Output:               capture.Output,
		Truncated:            capture.Truncated,
		TotalBytes:           capture.TotalBytes,
		TotalLines:           capture.TotalLines,
		RetainedBytes:        capture.RetainedBytes,
		OmittedBytes:         capture.OmittedBytes,
		CaptureLimitBytes:    capture.CaptureLimitBytes,
		CaptureLimitDisabled: capture.CaptureLimitDisabled,
	}
}

func applyCommandExecutionShell(result CommandExecutionResult, shell runtimeexecutor.Shell) CommandExecutionResult {
	result.ShellType = strings.TrimSpace(string(shell.Type))
	result.ShellPath = strings.TrimSpace(shell.Path)
	return result
}

func applyCommandTimeoutBudget(result *CommandExecutionResult, budget runtimeexecution.TimeoutBudget) {
	if result == nil {
		return
	}
	result.TimeoutRequestedMs = budget.Requested.Milliseconds()
	result.TimeoutEffectiveMs = budget.Effective.Milliseconds()
	result.TimeoutMs = result.TimeoutEffectiveMs
	result.TimeoutSource = string(budget.Source)
}

func buildCommandExecutionMetadata(command string, mutatedPaths []string, result CommandExecutionResult) map[string]interface{} {
	retainedBytes := result.RetainedBytes
	if retainedBytes == 0 && result.Output != "" {
		retainedBytes = len(result.Output)
	}
	totalBytes := result.TotalBytes
	if totalBytes == 0 && result.Output != "" {
		totalBytes = len(result.Output)
	}
	totalLines := result.TotalLines
	if totalLines == 0 && strings.TrimSpace(result.Output) != "" {
		totalLines = strings.Count(strings.ReplaceAll(result.Output, "\r\n", "\n"), "\n") + 1
	}
	metadata := map[string]interface{}{
		"command":                       command,
		"output_size":                   len(result.Output),
		"captured_output_bytes":         retainedBytes,
		"retained_output_bytes":         retainedBytes,
		"total_output_bytes":            totalBytes,
		"total_output_lines":            totalLines,
		"output_capture_complete":       !result.Truncated,
		"output_truncated":              result.Truncated,
		"capture_limit_reached":         result.Truncated,
		"output_capture_limit_disabled": result.CaptureLimitDisabled,
		"executed_at":                   time.Now().Unix(),
	}
	if !result.CaptureLimitDisabled && result.CaptureLimitBytes > 0 {
		metadata["output_capture_limit_bytes"] = result.CaptureLimitBytes
	}
	if result.OmittedBytes > 0 {
		metadata["omitted_output_bytes"] = result.OmittedBytes
	}
	if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
		metadata["raw_output_artifact_path"] = result.RawOutputArtifactPath
	}
	if strings.TrimSpace(result.RawOutputArtifactError) != "" {
		metadata["raw_output_artifact_error"] = result.RawOutputArtifactError
	}
	if result.TimeoutMs > 0 {
		metadata["timeout_ms"] = result.TimeoutMs
	}
	if result.TimeoutRequestedMs > 0 {
		metadata["timeout_requested_ms"] = result.TimeoutRequestedMs
	}
	if result.TimeoutEffectiveMs > 0 {
		metadata["timeout_effective_ms"] = result.TimeoutEffectiveMs
	}
	if strings.TrimSpace(result.TimeoutSource) != "" {
		metadata["timeout_source"] = strings.TrimSpace(result.TimeoutSource)
	}
	shell := runtimeexecutor.Shell{
		Type: runtimeexecutor.ShellType(strings.TrimSpace(result.ShellType)),
		Path: strings.TrimSpace(result.ShellPath),
	}
	for key, value := range shell.Metadata() {
		metadata[key] = value
	}
	if len(mutatedPaths) > 0 {
		metadata["mutated_paths"] = mutatedPaths
	}
	return metadata
}

func ensureLargeHistoryOutputArtifact(capture runtimeexecutor.CombinedOutputCapture, artifactPath string, artifactErr error, scope string, command string, preferredRoot string) (string, error) {
	if strings.TrimSpace(artifactPath) != "" || artifactErr != nil || capture.Truncated {
		return artifactPath, artifactErr
	}
	if capture.TotalBytes <= modelHistoryArtifactThresholdBytes || strings.TrimSpace(capture.Output) == "" {
		return artifactPath, artifactErr
	}
	path, err := runtimeexecutor.PersistShellOutputArtifact(scope, command, preferredRoot, capture.Output)
	if err != nil {
		return "", err
	}
	return path, nil
}

// resolveWorkdir resolves the working directory for command execution.
// If workdir is empty, returns the current working directory.
// If workdir is relative, joins it to the current working directory.
// If workdir is absolute, uses it as-is.
func resolveWorkdir(workdir string) (string, error) {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return os.Getwd()
	}
	if filepath.IsAbs(workdir) {
		return filepath.Clean(workdir), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	return filepath.Clean(filepath.Join(cwd, workdir)), nil
}

// prefixPowershellUTF8 prepends a UTF-8 encoding command for PowerShell
// to ensure output is correctly encoded, mirroring codex-rs/shell-command/src/powershell.rs.
func prefixPowershellUTF8(cmd *exec.Cmd) {
	if len(cmd.Args) < 3 {
		return
	}
	// The command is the last arg; prepend UTF-8 encoding directive
	lastIdx := len(cmd.Args) - 1
	cmd.Args[lastIdx] = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + cmd.Args[lastIdx]
}

// friendlyHintFor returns a user-friendly hint when a command fails.
func friendlyHintFor(command string, output string, err error, workdir string) string {
	cmdLower := strings.ToLower(command)
	outputLower := strings.ToLower(output)
	cmdParts := runtimeexecutor.SplitCommandTokens(command)
	mainCmd := ""
	if len(cmdParts) > 0 {
		mainCmd = strings.ToLower(cmdParts[0])
	}

	exitCode := -1
	if exitError, ok := err.(*exec.ExitError); ok {
		exitCode = exitError.ExitCode()
	}

	switch {
	case mainCmd == "pwd" && runtimeexecutor.IsWindows():
		shell := runtimeexecutor.DefaultUserShell()
		if shell.Type == runtimeexecutor.ShellTypeCmd {
			return "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
		}
		return ""
	case runtimeexecutor.IsWindows() &&
		(runtimeexecutor.HasPipedHeadToken(cmdParts) ||
			mainCmd == "head" ||
			strings.Contains(outputLower, "the term 'head' is not recognized")):
		return "提示: Windows PowerShell/pwsh 默认没有 `head`；请改用 `Select-Object -First 200`，例如 `git diff ... | Select-Object -First 200`。"
	case mainCmd == "ls" && runtimeexecutor.IsWindows():
		return "提示: Windows 下请使用 `dir` 查看目录内容"
	case mainCmd == "uname" && runtimeexecutor.IsWindows():
		return "提示: Windows 下请使用 `ver` 或 `systeminfo` 查看系统信息"
	case mainCmd == "cat" && runtimeexecutor.IsWindows() && !strings.Contains(cmdLower, "."):
		return "提示: Windows 下请使用 `type` 查看文件内容"
	case exitCode == 127:
		return "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"
	case strings.Contains(outputLower, "permission") ||
		strings.Contains(outputLower, "access") && strings.Contains(outputLower, "denied"):
		return "提示: 权限不足，请检查是否有执行该命令的权限"
	case strings.Contains(outputLower, "no such file or directory") ||
		strings.Contains(outputLower, "cannot find the path") ||
		strings.Contains(outputLower, "cannot find the file specified") ||
		strings.Contains(outputLower, "path not found"):
		if hint := runtimeexecutor.BuildPathNotFoundHintFromTokens(cmdParts, workdir); hint != "" {
			return hint
		}
		if trimmed := strings.TrimSpace(workdir); trimmed != "" {
			return fmt.Sprintf("提示: 文件或目录不存在，请先确认当前 workdir=%s 以及相对路径是否正确", trimmed)
		}
		return "提示: 文件或目录不存在"
	}
	return ""
}

// GetShellEnvironmentInfo returns environment info for error messages,
// including the detected shell type.
func GetShellEnvironmentInfo() string {
	shell := runtimeexecutor.DefaultUserShell()
	var parts []string
	parts = append(parts, fmt.Sprintf("系统类型: %s", runtimeexecutor.GoOS()))
	parts = append(parts, fmt.Sprintf("Shell: %s (%s)", shell.Type, shell.Path))
	return strings.Join(parts, "\n")
}

func (s outputCaptureSettings) captureLimitBytes() int {
	if s.disableOutputCap {
		return runtimeexecutor.DisableRetainedOutputLimit
	}
	if s.hasOutputBytesCap {
		return s.outputBytesCap
	}
	return runtimeexecutor.DefaultRetainedOutputBytes
}

func captureLimitBytesFromExecConfig(cfg *execConfig) int {
	if cfg == nil {
		return runtimeexecutor.DefaultRetainedOutputBytes
	}
	settings := outputCaptureSettings{
		outputBytesCap:    cfg.outputBytesCap,
		hasOutputBytesCap: cfg.hasOutputBytesCap,
		disableOutputCap:  cfg.disableOutputCap,
	}
	return settings.captureLimitBytes()
}

func parseOutputCaptureSettings(params map[string]interface{}) (outputCaptureSettings, error) {
	settings := outputCaptureSettings{}
	if params == nil {
		return settings, nil
	}

	if rawDisable, ok := params["disable_output_cap"]; ok {
		if rawDisable != nil {
			disable, ok := rawDisable.(bool)
			if !ok {
				return settings, fmt.Errorf("disable_output_cap 参数必须是布尔值")
			}
			settings.disableOutputCap = disable
		}
	}

	if rawCap, ok := params["output_bytes_cap"]; ok {
		if rawCap != nil && !isNumericZero(rawCap) {
			value, err := extractPositiveInt(rawCap)
			if err != nil {
				return settings, fmt.Errorf("output_bytes_cap 参数无效: %w", err)
			}
			settings.outputBytesCap = value
			settings.hasOutputBytesCap = true
		}
	}

	if settings.disableOutputCap && settings.hasOutputBytesCap {
		settings.disableOutputCap = false
	}

	return settings, nil
}

func parseShellCommandTimeout(params map[string]interface{}, defaultTimeout time.Duration) (time.Duration, error) {
	if defaultTimeout <= 0 {
		defaultTimeout = resolveDefaultShellCommandTimeout()
	}
	if params == nil {
		return defaultTimeout, nil
	}

	if raw, ok := params["timeout_ms"]; ok && raw != nil && !isNumericZero(raw) {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_ms 参数无效: %w", err)
		}
		return time.Duration(value) * time.Millisecond, nil
	}
	if raw, ok := params["timeout_sec"]; ok && raw != nil && !isNumericZero(raw) {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_sec 参数无效: %w", err)
		}
		return time.Duration(value) * time.Second, nil
	}
	if raw, ok := params["timeout"]; ok && raw != nil {
		timeoutText, ok := raw.(string)
		if !ok {
			return defaultTimeout, nil
		}
		timeoutText = strings.TrimSpace(timeoutText)
		if timeoutText == "" {
			return defaultTimeout, nil
		}
		if seconds, numberErr := strconv.ParseFloat(timeoutText, 64); numberErr == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second)), nil
		}
		parsed, err := time.ParseDuration(timeoutText)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("timeout 参数无效: %q", timeoutText)
		}
		return parsed, nil
	}
	return defaultTimeout, nil
}

func inferredShellCommandTimeout(command string, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = resolveDefaultShellCommandTimeout()
	}
	fields := strings.Fields(strings.ToLower(command))
	for index := 0; index+1 < len(fields); index++ {
		if (fields[index] == "go" || fields[index] == "go.exe") && fields[index+1] == "test" {
			if fallback < defaultGoTestCommandTimeout {
				return defaultGoTestCommandTimeout
			}
			break
		}
	}
	return fallback
}

func hasExplicitShellTimeout(params map[string]interface{}) bool {
	for _, key := range []string{"timeout_ms", "timeout_sec", "timeout"} {
		if value, ok := params[key]; ok && value != nil {
			if isNumericZero(value) {
				continue
			}
			if text, isText := value.(string); !isText || strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

func shellCommandTimeoutError(timeout time.Duration) error {
	return fmt.Errorf("命令执行超时（超过 %v）。如需继续运行长命令，请重试并显式设置 timeout（例如 2m、5m）或 timeout_ms/timeout_sec", timeout)
}

func resolveDefaultShellCommandTimeout() time.Duration {
	return resolveShellTimeoutFromEnv(shellCommandTimeoutMSEnv, shellCommandTimeoutEnv, defaultShellCommandTimeout)
}

func resolveShellTimeoutFromEnv(timeoutMSEnv string, timeoutEnv string, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = defaultShellCommandTimeout
	}
	if raw := strings.TrimSpace(os.Getenv(timeoutMSEnv)); raw != "" {
		value, err := time.ParseDuration(raw + "ms")
		if err == nil && value > 0 {
			return value
		}
	}
	if raw := strings.TrimSpace(os.Getenv(timeoutEnv)); raw != "" {
		value, err := time.ParseDuration(raw)
		if err == nil && value > 0 {
			return value
		}
	}
	return fallback
}

// extractString extracts a string value from a map, returning "" if not found.
func extractString(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func extractPositiveInt(value interface{}) (int, error) {
	switch typed := value.(type) {
	case int:
		if typed <= 0 {
			return 0, fmt.Errorf("必须为正整数")
		}
		return typed, nil
	case int32:
		if typed <= 0 {
			return 0, fmt.Errorf("必须为正整数")
		}
		return int(typed), nil
	case int64:
		if typed <= 0 {
			return 0, fmt.Errorf("必须为正整数")
		}
		return int(typed), nil
	case float32:
		if typed <= 0 || float32(int(typed)) != typed {
			return 0, fmt.Errorf("必须为正整数")
		}
		return int(typed), nil
	case float64:
		if typed <= 0 || float64(int(typed)) != typed {
			return 0, fmt.Errorf("必须为正整数")
		}
		return int(typed), nil
	default:
		return 0, fmt.Errorf("必须为正整数")
	}
}

func isNumericZero(value interface{}) bool {
	switch typed := value.(type) {
	case int:
		return typed == 0
	case int32:
		return typed == 0
	case int64:
		return typed == 0
	case uint:
		return typed == 0
	case uint32:
		return typed == 0
	case uint64:
		return typed == 0
	case float32:
		return typed == 0
	case float64:
		return typed == 0
	default:
		return false
	}
}

func extractStringList(value interface{}) []string {
	if value == nil {
		return nil
	}
	out := make([]string, 0)
	switch items := value.(type) {
	case []string:
		for _, item := range items {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []interface{}:
		for _, item := range items {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractPrimaryCommand(command string) string {
	parts := runtimeexecutor.SplitCommandTokens(command)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
