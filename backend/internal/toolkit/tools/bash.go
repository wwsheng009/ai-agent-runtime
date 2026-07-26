package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	stderrors "errors"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
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
	defaultSearchShellCommandTimeout   = 12 * time.Second
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
				"description": "单条 Shell 命令。需要顺序执行多条独立检查时改用 commands，减少 LLM 往返。用 workdir 切换目录；Windows PowerShell 没有 head 时使用 Select-Object。代码搜索请优先用 toolkit grep，不要用 shell rg/grep。Windows 不要使用 bash heredoc（<<EOF）。",
			},
			"commands": map[string]interface{}{
				"type":        "array",
				"description": "命令批次（对象数组，也可容忍 JSON 字符串数组）。默认顺序执行；仅当各命令互不依赖且只读时可设置 parallel=true。每项可覆盖 workdir 和 timeout。",
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
				"description": "commands 批次中某条硬失败（未启动/超时/取消/权限拒绝等）后是否停止；默认 false。进程非零退出码视为内容结果，不会触发停止。",
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
				"description": "可选：命令超时，例如 30s、2m、5m。普通命令默认 30s；go test 未显式设置时自动使用至少 5m；shell 代码搜索（rg/grep/findstr）未显式设置时默认更短（约 12s）以促使改用 toolkit grep；环境变量和显式参数仍可覆盖。",
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
			"执行一条 Shell 命令，或用 commands 顺序执行一批检查并一次返回全部结果。仅当后续命令依赖前一条输出且需模型决策时才拆分。进程非零退出码会作为内容结果返回（含 Exit code/Output），不是工具崩溃；仅未启动/超时/取消/权限拒绝等才是硬失败。代码搜索优先用 toolkit `grep`（rg 在 shell 中 exit 1=无匹配、且易因引号/正则转义失败）；文件系统查看优先 ls/glob/view，不要把 view/grep/ls 写成 shell 命令。Windows 默认 PowerShell，没有 head 时用 Select-Object；不要使用 bash heredoc（<<EOF）。",
			"1.3.4",
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
		runtimetypes.ToolMetadataKindKey:            runtimetypes.ToolKindExec,
		runtimetypes.ToolMetadataReadOnlyKey:        false,
		runtimetypes.ToolMetadataMutatesFSKey:       false,
		runtimetypes.ToolMetadataRequiresNetKey:     false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
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
			Error:      buildBashMissingCommandError(params),
		}, nil
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      buildBashEmptyCommandError(params),
		}, nil
	}
	if blocked, message, nextAction := bashCommandPreflight(command); blocked {
		return toolResultFailureWithCode(
			fmt.Errorf("%s", message),
			string(runtimeerrors.ErrToolShellCompat),
			nextAction,
			map[string]interface{}{
				"failure_class": "shell_preflight",
			},
		), nil
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
	started := time.Now()
	execResult, err := b.executeCommand(ctx, command, workdir, timeout, captureSettings)
	duration := time.Since(started)
	if err != nil {
		// rg/grep exit 1 with empty/no-error output is "no matches", not a hard
		// crash. Soft-succeed so batch checks and model recovery treat it as
		// empty evidence instead of failing the whole tool call.
		if isSearchToolNoMatch(command, execResult.Output, err) {
			metadata := buildCommandExecutionMetadata(command, mutatedPaths, execResult)
			toolresult.MarkEmptySuccess(metadata)
			metadata["search_no_match"] = true
			metadata["exit_code"] = 1
			metadata["non_zero_exit"] = true
			if duration > 0 {
				metadata["duration_ms"] = duration.Milliseconds()
			}
			metadata[toolresult.MetadataNextActionKey] = bashSearchNoMatchNextAction()
			// Prefer the cleaned search body so PowerShell NativeCommandError chrome
			// is not presented as useful stdout to the model.
			content := strings.TrimSpace(stripPowerShellNoiseForSearchClassification(execResult.Output))
			if content == "" {
				content = "未匹配到结果（rg/grep exit 1）。这是空证据，不是命令崩溃。优先改用 toolkit `grep`，或更换关键词/扩大 path。"
			} else if hint := friendlyHintFor(command, execResult.Output, err, workdir); hint != "" {
				content = strings.TrimSpace(content + "\n" + hint)
			}
			content = formatShellCommandContent(1, execResult.ShellType, workdir, duration, false, content)
			return &toolkit.ToolResult{
				Success:    true,
				OutputKind: toolresult.KindText,
				Content:    content,
				Metadata:   metadata,
			}, nil
		}
		// Completed process with a non-zero exit code is content success (Codex-
		// like contract). Models must inspect Exit code / Output rather than
		// treating every non-zero status as TOOL_EXECUTION.
		if !isHardShellExecutionError(err) {
			exitCode := exitCodeFromError(err)
			if exitCode < 0 {
				exitCode = 1
			}
			metadata := buildCommandExecutionMetadata(command, mutatedPaths, execResult)
			metadata["exit_code"] = exitCode
			metadata["non_zero_exit"] = exitCode != 0
			if duration > 0 {
				metadata["duration_ms"] = duration.Milliseconds()
			}
			if next := bashCommandFailureNextAction(command, execResult.Output, err); next != "" {
				metadata[toolresult.MetadataNextActionKey] = next
			}
			content := formatShellCommandContent(exitCode, execResult.ShellType, workdir, duration, false, execResult.Output)
			if hint := friendlyHintFor(command, execResult.Output, err, workdir); hint != "" {
				content = strings.TrimRight(content, "\n") + "\n" + hint
			}
			return &toolkit.ToolResult{
				Success:    true,
				OutputKind: toolresult.KindText,
				Content:    content,
				Metadata:   metadata,
			}, nil
		}
		failureMetadata := buildCommandExecutionMetadata(command, mutatedPaths, execResult)
		if duration > 0 {
			failureMetadata["duration_ms"] = duration.Milliseconds()
		}
		if code := exitCodeFromError(err); code >= 0 {
			failureMetadata["exit_code"] = code
		}
		if next := bashCommandFailureNextAction(command, execResult.Output, err); next != "" {
			failureMetadata[toolresult.MetadataNextActionKey] = next
		}
		code := classifyHardShellExecutionErrorCode(err, execResult.Output)
		result := toolResultFailureWithCode(
			buildBashCommandFailureError(command, execResult.Output, err),
			code,
			"", // keep bashCommandFailureNextAction from failureMetadata when present
			failureMetadata,
		)
		// Preserve partial stdout/stderr for model recovery even on hard fail.
		result.Content = execResult.Output
		return result, nil
	}

	metadata := buildCommandExecutionMetadata(command, mutatedPaths, execResult)
	metadata["exit_code"] = 0
	if duration > 0 {
		metadata["duration_ms"] = duration.Milliseconds()
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    formatShellCommandContent(0, execResult.ShellType, workdir, duration, false, execResult.Output),
		Metadata:   metadata,
	}, nil
}

func parseBashCommandBatch(params map[string]interface{}) ([]bashCommandBatchItem, bool, error) {
	raw, exists := params["commands"]
	if !exists || raw == nil {
		return nil, false, nil
	}
	// Some providers/models stringify optional array fields. Accept a JSON array
	// string (or single command string) instead of forcing another round-trip.
	if coerced, ok, err := coerceBashCommandsParam(raw); err != nil {
		return nil, true, err
	} else if ok {
		raw = coerced
	}
	values, ok := raw.([]interface{})
	if !ok {
		if typed, typedOK := raw.([]map[string]interface{}); typedOK {
			values = make([]interface{}, 0, len(typed))
			for _, item := range typed {
				values = append(values, item)
			}
		} else if command, isString := raw.(string); isString {
			command = strings.TrimSpace(command)
			if command == "" {
				return nil, true, fmt.Errorf("commands 参数必须是对象数组")
			}
			// Single string command coerced into a one-item batch.
			return []bashCommandBatchItem{{Command: command, Params: map[string]interface{}{}}}, true, nil
		} else {
			return nil, true, fmt.Errorf("commands 参数必须是对象数组（或 JSON 数组字符串）")
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
	failedItems := make([]map[string]interface{}, 0)
	failed := 0
	nonZeroExit := 0
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
			if result.Metadata != nil {
				if code, ok := result.Metadata["exit_code"]; ok {
					entry["exit_code"] = code
					if asInt, ok := asIntish(code); ok && asInt != 0 {
						nonZeroExit++
						entry["non_zero_exit"] = true
						if result.Success {
							status = "exit_nonzero"
						}
					}
				}
			}
			if artifactPath := extractString(result.Metadata["raw_output_artifact_path"]); artifactPath != "" {
				artifactPaths = append(artifactPaths, artifactPath)
			}
		}
		if result == nil || !result.Success {
			failed++
			status = "failed"
			errText := ""
			if result != nil && result.Error != nil {
				errText = result.Error.Error()
				entry["error"] = errText
				if strings.TrimSpace(content) == "" {
					content = errText
				}
			}
			if errText == "" {
				errText = "unknown error"
			}
			if row := toolresult.FailedItemMap(toolresult.IntPtr(index), "", item.Command, errText); row != nil {
				failedItems = append(failedItems, row)
			}
		}
		sections = append(sections, fmt.Sprintf("===== command %d/%d [%s] =====\n%s\n%s", index+1, len(commands), status, item.Command, strings.TrimSpace(content)))
		items = append(items, entry)
	}
	succeeded := len(commands) - failed
	if succeeded < 0 {
		succeeded = 0
	}
	metadata := map[string]interface{}{
		"batch": true, "requested_count": len(commands), "executed_count": len(items),
		"succeeded_count": succeeded, "failed_count": failed,
		"partial_failure": failed > 0 && succeeded > 0,
		"stop_on_error":   stopOnError, "items": items,
		"parallel": parallelism > 1, "parallelism": parallelism,
		"command": strings.Join(commandTexts, "\n"), "commands": commandTexts,
	}
	if nonZeroExit > 0 {
		metadata["non_zero_exit_count"] = nonZeroExit
		metadata["has_non_zero_exit"] = true
	}
	if len(failedItems) > 0 {
		// Source-side failed_items so gateway/Diagnose can enrich next_action
		// without parsing items[] or error strings.
		metadata[toolresult.MetadataFailedItemsKey] = failedItems
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
			Metadata: metadata, Error: buildBashBatchFailureError(failed, items),
		}, nil
	}
	// Process non-zero exits are already content-success per item. Keep the
	// batch tool call successful and surface recovery guidance in metadata.
	if nonZeroExit > 0 {
		metadata[toolresult.MetadataNextActionKey] = "One or more shell commands exited non-zero. Inspect each Exit code/Output section, reuse successful item outputs, and only retry the failed commands with a changed command/workdir/timeout. Non-zero exit is not a tool crash."
		if failed == 0 && succeeded == len(commands) {
			// All items completed; mark outcome-friendly partial only when mixed
			// zero/non-zero is useful. Pure non-zero batch still succeeds.
			if nonZeroExit < len(commands) {
				metadata["partial_failure"] = false
				metadata["partial_non_zero_exit"] = true
			}
		}
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

	// Optional OS-level wrap (Linux bubblewrap when configured). Application-
	// layer policy already ran above; auto degrades with warnings, require fails closed.
	if launch, wrapErr := b.sandbox.PrepareOSCommand(cmdCtx, shellCmd[0], shellCmd[1:], resolvedWorkdir, cmd.Env); wrapErr != nil {
		return CommandExecutionResult{}, wrapSandboxPermissionError("sandbox denied os isolation", wrapErr, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
			"launcher":  shellCmd[0],
			"os_mode":   b.sandbox.Config().OSSandbox,
		})
	} else {
		cmd = exec.CommandContext(cmdCtx, launch.Command, launch.Args...)
		if strings.TrimSpace(launch.WorkDir) != "" {
			cmd.Dir = launch.WorkDir
		} else if !launch.Applied {
			cmd.Dir = resolvedWorkdir
		}
		if launch.Env != nil {
			cmd.Env = launch.Env
		}
		// Surface explicit degrade notices in structured metadata when present.
		_ = launch.Warnings
	}

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
	// Prefer the first search tool token even when the command is wrapped
	// (e.g. "rg ... | Select-Object -First 20" or shell prefixes).
	searchCmd := firstSearchToolToken(cmdParts)
	if searchCmd == "" {
		searchCmd = mainCmd
	}

	exitCode := -1
	if exitError, ok := err.(*exec.ExitError); ok {
		exitCode = exitError.ExitCode()
	} else {
		exitCode = exitCodeFromError(err)
	}

	switch {
	case mainCmd == "pwd" && runtimeexecutor.IsWindows():
		shell := runtimeexecutor.DefaultUserShell()
		if shell.Type == runtimeexecutor.ShellTypeCmd {
			return "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
		}
		return ""
	case runtimeexecutor.IsWindows() && looksLikeBashHeredoc(command):
		return "提示: Windows PowerShell/cmd 不支持 bash heredoc（<<EOF）。请用 write/append_write 写临时脚本后执行，或改用 python -c / 专用文件工具。"
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
	case isRipgrepOrGrepTool(searchCmd) ||
		strings.Contains(outputLower, "regex parse error") ||
		looksLikeSearchPathShellGlob(command) ||
		searchOutputLooksLikePathIOError(output):
		if hint := friendlyHintForSearchTool(searchCmd, command, output, exitCode); hint != "" {
			return hint
		}
	case exitCode == 127:
		return "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"
	case strings.Contains(outputLower, "permission") ||
		strings.Contains(outputLower, "access") && strings.Contains(outputLower, "denied"):
		return "提示: 权限不足，请检查是否有执行该命令的权限"
	case strings.Contains(outputLower, "no such file or directory") ||
		strings.Contains(outputLower, "cannot find the path") ||
		strings.Contains(outputLower, "cannot find the file specified") ||
		strings.Contains(outputLower, "path not found") ||
		strings.Contains(outputLower, "系统找不到指定的文件") ||
		strings.Contains(outputLower, "系统找不到指定的路径") ||
		strings.Contains(outputLower, "cannot find path") ||
		strings.Contains(outputLower, "itemnotfoundexception") ||
		strings.Contains(outputLower, "does not exist"):
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

func firstSearchToolToken(cmdParts []string) string {
	for _, part := range cmdParts {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(part)))
		base = strings.TrimSuffix(base, ".exe")
		if isRipgrepOrGrepTool(base) {
			return base
		}
	}
	return ""
}

func isRipgrepOrGrepTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rg", "ripgrep", "grep", "egrep", "fgrep":
		return true
	default:
		return false
	}
}

// friendlyHintForSearchTool explains common rg/grep failures that models treat
// as hard errors (regex parse, exit 1 = no matches). Prefer the dedicated grep tool.
func friendlyHintForSearchTool(toolName, command, output string, exitCode int) string {
	// Classify against cleaned search body so PowerShell NativeCommandError chrome
	// does not hide the "exit 1 == no matches" recovery path.
	cleaned := stripPowerShellNoiseForSearchClassification(output)
	cleanedLower := strings.ToLower(cleaned)
	rawLower := strings.ToLower(output)
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		toolName = "rg"
	}

	switch {
	case strings.Contains(cleanedLower, "regex parse error") ||
		strings.Contains(cleanedLower, "regex error") ||
		strings.Contains(cleanedLower, "invalid regex") ||
		strings.Contains(cleanedLower, "error parsing regex") ||
		// Fall back to raw output when noise strip kept nothing useful.
		strings.Contains(rawLower, "regex parse error") ||
		strings.Contains(rawLower, "regex error"):
		return fmt.Sprintf(
			"提示: %s 正则解析失败（常见于 JSON 参数里对引号/`|`/`()` 转义不完整）。优先改用 toolkit `grep`（可设 literal=true 做字面搜索，或 pcre2=true）；若必须 shell 调用，请用单引号包裹 pattern，或改用 `rg -F` 字面匹配。",
			toolName,
		)
	case looksLikeSearchPathShellGlob(command) ||
		searchOutputLooksLikePathIOError(output) ||
		searchOutputLooksLikePathIOError(cleaned):
		return fmt.Sprintf(
			"提示: %s 路径参数含 shell 通配符或路径 IO 失败（Windows 常见 os error 123 / 文件名语法不正确）。不要写 `rg pattern backend/**/*.go`；改用 `rg -g \"*.go\" pattern backend`，或优先 toolkit `grep` 的 path/glob。命令: %s",
			toolName,
			truncateDiagnosticText(strings.TrimSpace(command), 160),
		)
	case exitCode == 1:
		// rg/grep exit 1 means "no matches" when there is no real error body.
		// Models often retry uselessly; point them at the dedicated grep tool.
		if strings.TrimSpace(cleaned) == "" ||
			(!strings.Contains(cleanedLower, "error:") &&
				!strings.Contains(cleanedLower, "failed:") &&
				!strings.Contains(cleanedLower, "denied") &&
				!strings.Contains(cleanedLower, "no such file") &&
				!strings.Contains(cleanedLower, "cannot find path") &&
				!searchOutputLooksLikePathIOError(cleaned)) {
			return fmt.Sprintf(
				"提示: %s 退出码 1 通常表示未匹配到结果（不是命令崩溃）。若要搜索代码，优先用 toolkit `grep`（支持 literal/paths/glob，错误信息更可行动）；确认无结果后换关键词或扩大 path。命令: %s",
				toolName,
				truncateDiagnosticText(strings.TrimSpace(command), 160),
			)
		}
	case exitCode == 2:
		return fmt.Sprintf(
			"提示: %s 退出码 2 通常表示用法/路径/正则错误。优先改用 toolkit `grep`；检查 path 是否存在，pattern 是否需要 literal=true；Windows 下勿把 `*.go` 放在 path 位置，改用 `-g`。",
			toolName,
		)
	}
	return ""
}

// isSearchToolNoMatch reports whether a failed shell command is the common
// rg/grep "exit 1 == no matches" case rather than a real crash.
func isSearchToolNoMatch(command, output string, err error) bool {
	if err == nil {
		return false
	}
	// Timeouts/cancels are real control-plane failures, never empty-search.
	if runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) ||
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, context.Canceled) {
		return false
	}
	cmdParts := runtimeexecutor.SplitCommandTokens(command)
	searchCmd := firstSearchToolToken(cmdParts)
	if searchCmd == "" {
		// Also accept search commands detected by looser heuristics when token
		// splitting is noisy (quoted pipelines / PowerShell wrappers).
		if !looksLikeSearchShellCommand(command) {
			return false
		}
	}
	exitCode := exitCodeFromError(err)
	if exitCode != 1 {
		return false
	}
	// Strip PowerShell wrapper noise before classifying real search errors.
	// NativeCommandError records often contain the substring "error" even when
	// the underlying rg/grep simply found no matches under $ErrorActionPreference.
	cleaned := stripPowerShellNoiseForSearchClassification(output)
	cleanedLower := strings.ToLower(cleaned)
	// Path/IO failures (including Windows os error 123 on unexpanded globs) must
	// never soft-succeed as empty search, even when exit code is 1.
	if searchOutputLooksLikePathIOError(cleaned) || searchOutputLooksLikePathIOError(output) {
		return false
	}
	if strings.TrimSpace(cleaned) != "" &&
		(strings.Contains(cleanedLower, "regex parse") ||
			strings.Contains(cleanedLower, "regex error") ||
			strings.Contains(cleanedLower, "invalid regex") ||
			strings.Contains(cleanedLower, "error parsing regex") ||
			strings.Contains(cleanedLower, "no such file") ||
			strings.Contains(cleanedLower, "cannot find path") ||
			strings.Contains(cleanedLower, "access is denied") ||
			strings.Contains(cleanedLower, "permission denied") ||
			strings.Contains(cleanedLower, "is not recognized") ||
			// Remaining bare "error:" / "failed:" after noise strip are real.
			strings.Contains(cleanedLower, "error:") ||
			strings.Contains(cleanedLower, "failed:") ||
			strings.Contains(cleanedLower, "denied") ||
			strings.Contains(cleanedLower, "io error") ||
			strings.Contains(cleanedLower, "os error")) {
		return false
	}
	return true
}

// searchOutputLooksLikePathIOError detects rg/grep path IO failures that models
// often create by putting shell globs in the path position on Windows.
func searchOutputLooksLikePathIOError(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "io error") ||
		strings.Contains(lower, "os error") ||
		strings.Contains(lower, "os error 123") ||
		strings.Contains(output, "文件名、目录名或卷标语法不正确") ||
		strings.Contains(output, "语法不正确") ||
		strings.Contains(lower, "filename, directory name, or volume label syntax is incorrect") ||
		strings.Contains(lower, "the filename, directory name, or volume label syntax is incorrect")
}

// stripPowerShellNoiseForSearchClassification removes common PowerShell error-
// record framing so soft-success can still recognize rg/grep no-match cases.
func stripPowerShellNoiseForSearchClassification(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	text := strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		// Drop ANSI/PowerShell error-record chrome that is not the search tool body.
		if strings.Contains(lower, "nativecommanderror") ||
			strings.Contains(lower, "parentcontainserrorrecordexception") ||
			strings.Contains(lower, "categoryinfo") ||
			strings.Contains(lower, "fullyqualifiederrorid") ||
			strings.Contains(lower, "remoteexception") ||
			strings.HasPrefix(lower, "+ categoryinfo") ||
			strings.HasPrefix(lower, "+ fullyqualifiederrorid") ||
			// Common pwsh formatting lines around NativeCommandError.
			strings.HasPrefix(lower, "line |") ||
			(strings.HasPrefix(trimmed, "+") && strings.Contains(lower, "~~~")) {
			continue
		}
		// Strip residual ANSI sequences for keyword checks.
		cleaned := ansiEscapeSequencePattern.ReplaceAllString(trimmed, "")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" || cleaned == "---" {
			continue
		}
		kept = append(kept, cleaned)
	}
	return strings.Join(kept, "\n")
}

// ansiEscapeSequencePattern matches CSI/OSC-like ANSI sequences used in pwsh errors.
var ansiEscapeSequencePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*(\x07|\x1b\\)`)

// exitCodeFromError extracts a process exit code from *exec.ExitError or from
// common "exit status N" / Windows hex status strings. Used when errors are
// wrapped or when only the message remains after shell layers.
func exitCodeFromError(err error) int {
	if err == nil {
		return -1
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	msg := strings.TrimSpace(err.Error())
	// "exit status 1" / "exit status 0x80008083"
	const prefix = "exit status "
	if idx := strings.LastIndex(strings.ToLower(msg), prefix); idx >= 0 {
		token := strings.TrimSpace(msg[idx+len(prefix):])
		// stop at first non-code char
		end := 0
		for end < len(token) {
			c := token[end]
			if (c >= '0' && c <= '9') || c == 'x' || c == 'X' ||
				(c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
				end++
				continue
			}
			break
		}
		token = token[:end]
		if token == "" {
			return -1
		}
		if strings.HasPrefix(strings.ToLower(token), "0x") {
			if v, parseErr := strconv.ParseInt(token[2:], 16, 64); parseErr == nil {
				return int(v)
			}
			return -1
		}
		if v, parseErr := strconv.Atoi(token); parseErr == nil {
			return v
		}
	}
	return -1
}

// isHardShellExecutionError reports true only for control-plane / launch failures
// where the shell tool itself could not complete a normal process run. A finished
// process with non-zero exit code is NOT a hard failure.
func isHardShellExecutionError(err error) bool {
	if err == nil {
		return false
	}
	if runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) ||
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, context.Canceled) {
		return true
	}
	// Friendly wrappers around *exec.ExitError still parse as exit status.
	if exitCodeFromError(err) >= 0 {
		return false
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return false
	}
	return true
}

// classifyHardShellExecutionErrorCode maps control-plane / launch failures to a
// stable runtime error_code. Finished non-zero exits never reach this path.
func classifyHardShellExecutionErrorCode(err error, output string) string {
	if err == nil {
		return ""
	}
	switch {
	case runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout),
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded),
		stderrors.Is(err, context.DeadlineExceeded):
		return string(runtimeerrors.ErrToolTimeout)
	case stderrors.Is(err, context.Canceled),
		runtimeerrors.Is(err, runtimeerrors.ErrAgentRunCanceled):
		return string(runtimeerrors.ErrAgentRunCanceled)
	}
	combined := strings.ToLower(strings.TrimSpace(err.Error() + "\n" + output))
	switch {
	case strings.Contains(combined, "permission denied"),
		strings.Contains(combined, "access is denied"),
		strings.Contains(combined, "operation not permitted"),
		strings.Contains(combined, "denied by policy"):
		return string(runtimeerrors.ErrAgentPermission)
	case strings.Contains(combined, "executable file not found"),
		strings.Contains(combined, "not recognized as"),
		strings.Contains(combined, "is not recognized"),
		strings.Contains(combined, "command not found"),
		strings.Contains(combined, "no such file or directory") && strings.Contains(combined, "exec"),
		strings.Contains(combined, "the term '") && strings.Contains(combined, "is not recognized"),
		strings.Contains(combined, "parsererror"),
		strings.Contains(combined, "here-string"),
		strings.Contains(combined, "heredoc"):
		return string(runtimeerrors.ErrToolShellCompat)
	case strings.Contains(combined, "failed to start"),
		strings.Contains(combined, "cannot run"),
		strings.Contains(combined, "exec:") && strings.Contains(combined, "not found"):
		return string(runtimeerrors.ErrProcessStartFailed)
	default:
		// Unknown launch/control failures remain generic tool execution.
		return string(runtimeerrors.ErrToolExecution)
	}
}

// formatShellCommandContent builds a Codex-like model-facing shell result body.
// Non-zero exits remain tool Success with this structured content.
func formatShellCommandContent(exitCode int, shellType, workdir string, duration time.Duration, timedOut bool, output string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exit code: %d\n", exitCode)
	if shell := strings.TrimSpace(shellType); shell != "" {
		fmt.Fprintf(&b, "Shell: %s\n", shell)
	} else if detected := runtimeexecutor.DefaultUserShell(); strings.TrimSpace(string(detected.Type)) != "" {
		fmt.Fprintf(&b, "Shell: %s\n", detected.Type)
	}
	if wd := strings.TrimSpace(workdir); wd != "" {
		fmt.Fprintf(&b, "Workdir: %s\n", wd)
	}
	if duration > 0 {
		// Prefer compact wall time like Codex (e.g. 1.23s / 450ms).
		if duration < time.Second {
			fmt.Fprintf(&b, "Wall time: %dms\n", duration.Milliseconds())
		} else {
			fmt.Fprintf(&b, "Wall time: %.2fs\n", duration.Seconds())
		}
	}
	fmt.Fprintf(&b, "Timed out: %v\n", timedOut)
	fmt.Fprintf(&b, "Output:\n%s", output)
	return b.String()
}

// asIntish coerces common numeric JSON/metadata shapes to int.
func asIntish(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
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
	// Broad shell code searches often hang or thrash on large trees. Prefer a
	// shorter default so models recover via toolkit grep instead of waiting out
	// the full 30s shell budget. Explicit timeout still wins.
	if looksLikeSearchShellCommand(command) {
		if fallback > defaultSearchShellCommandTimeout {
			return defaultSearchShellCommandTimeout
		}
		return fallback
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

func buildBashMissingCommandError(params map[string]interface{}) error {
	parts := []string{"command 参数缺失或类型错误"}
	if parseErr := extractString(params["_parse_error"]); parseErr != "" {
		parts = append(parts, fmt.Sprintf("参数解析失败: %s。请缩短 command 或拆成 commands 批次后重试", parseErr))
	} else if raw := extractString(params["_raw"]); raw != "" {
		parts = append(parts, "检测到原始工具参数，但缺少可解析的 command 字段；请使用 {\"command\":\"...\"} 或 commands 数组")
	} else {
		parts = append(parts, "请提供非空 command 字符串，或改用 commands:[{command:\"...\"}] 批量执行")
	}
	return fmt.Errorf("%s", strings.Join(parts, "。"))
}

func buildBashEmptyCommandError(params map[string]interface{}) error {
	parts := []string{"command 参数为空"}
	if parseErr := extractString(params["_parse_error"]); parseErr != "" {
		parts = append(parts, fmt.Sprintf("参数解析失败: %s", parseErr))
	}
	parts = append(parts, "请提供非空 command，或改用 commands 批次")
	return fmt.Errorf("%s", strings.Join(parts, "。"))
}

// buildBashCommandFailureError enriches bare shell exit errors with command and
// output snippets so models can recover without re-reading only "exit status 1".
func buildBashCommandFailureError(command, output string, err error) error {
	if err == nil {
		return nil
	}
	// Preserve structured timeout/cancellation errors for runtimeerrors.Is checks.
	if runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) ||
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) ||
		stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, context.Canceled) {
		// Search-like timeouts should steer the model toward dedicated tools /
		// narrower scopes instead of blindly replaying the same broad scan.
		if looksLikeSearchShellCommand(command) {
			return fmt.Errorf("%w。命令: %s。代码搜索超时请改用 toolkit `grep`（可设 path/glob/max_count），或缩小 workdir/pattern；不要原样重试同一宽范围 shell 搜索",
				err, truncateDiagnosticText(strings.TrimSpace(command), 120))
		}
		return fmt.Errorf("%w。命令: %s。超时后先检查是否已有部分输出；缩小范围、提高 timeout，或改用更专用的工具，不要盲目重试同一命令",
			err, truncateDiagnosticText(strings.TrimSpace(command), 120))
	}
	base := strings.TrimSpace(err.Error())
	// friendlyHintFor already embeds 提示 / environment info; keep as-is.
	if strings.Contains(base, "提示:") || strings.Contains(base, "当前环境信息:") {
		return err
	}
	cmdPreview := truncateDiagnosticText(strings.TrimSpace(command), 120)
	snippet := firstNonEmptyLines(output, 4, 240)
	if snippet != "" {
		return fmt.Errorf("%s。命令: %s。输出摘要: %s。完整输出见 Content；批量检查请用 commands 并查看失败摘要", base, cmdPreview, snippet)
	}
	return fmt.Errorf("%s。命令: %s。无 stdout/stderr 输出；请检查命令拼写、workdir、PATH，以及是否需要先 cd 到正确目录", base, cmdPreview)
}

func firstNonEmptyLines(text string, maxLines, maxChars int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	picked := make([]string, 0, maxLines)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		picked = append(picked, line)
		if len(picked) >= maxLines {
			break
		}
	}
	if len(picked) == 0 {
		return truncateDiagnosticText(text, maxChars)
	}
	return truncateDiagnosticText(strings.Join(picked, " | "), maxChars)
}

func buildBashBatchFailureError(failed int, items []map[string]interface{}) error {
	parts := []string{fmt.Sprintf("bash command batch completed with %d failure(s)", failed)}
	summaries := make([]string, 0, 3)
	for _, item := range items {
		if success, _ := item["success"].(bool); success {
			continue
		}
		index, _ := item["index"].(int)
		command := truncateDiagnosticText(extractString(item["command"]), 80)
		errText := truncateDiagnosticText(extractString(item["error"]), 120)
		if errText == "" {
			errText = "unknown error"
		}
		summaries = append(summaries, fmt.Sprintf("#%d %s => %s", index+1, command, errText))
		if len(summaries) >= 3 {
			break
		}
	}
	if len(summaries) > 0 {
		parts = append(parts, "失败摘要: "+strings.Join(summaries, "; "))
	}
	parts = append(parts, "请查看 Content 中对应 command 段落输出后修复失败命令，或用 stop_on_error=true 在首个失败后停止")
	return fmt.Errorf("%s", strings.Join(parts, "。"))
}

func looksLikeSearchShellCommand(command string) bool {
	cmdParts := runtimeexecutor.SplitCommandTokens(command)
	if firstSearchToolToken(cmdParts) != "" {
		return true
	}
	lower := strings.ToLower(command)
	return strings.Contains(lower, "rg ") ||
		strings.Contains(lower, "rg.exe") ||
		strings.HasPrefix(strings.TrimSpace(lower), "rg") ||
		strings.Contains(lower, "grep ") ||
		strings.Contains(lower, "findstr ")
}

func bashSearchNoMatchNextAction() string {
	return "Treat this as no-match / empty evidence, not a hard crash. Prefer toolkit `grep` (literal/paths/glob) for code search; change pattern or broaden path/workdir. Do not retry the identical shell query unchanged."
}

// bashCommandFailureNextAction returns structured recovery guidance for hard
// shell failures. Search-like failures prefer the dedicated grep tool.
func bashCommandFailureNextAction(command, output string, err error) string {
	if err == nil {
		return ""
	}
	if runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) ||
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) ||
		stderrors.Is(err, context.DeadlineExceeded) {
		if looksLikeSearchShellCommand(command) {
			return "Code search timed out. Prefer toolkit `grep` with a narrower path/glob/max_count, or tighten workdir/pattern. Do not replay the same broad shell search unchanged."
		}
		return "Command timed out. Inspect any partial output, narrow the scope, raise timeout only if needed, or switch to a more specialized tool. Do not blindly retry the same command."
	}
	outputLower := strings.ToLower(output)
	errLower := strings.ToLower(err.Error())
	combined := outputLower + "\n" + errLower
	if looksLikeGitIgnoredPathFailure(command, combined) {
		return "Git refused a path that is ignored by .gitignore / exclude rules. Inspect with `git check-ignore -v <path>` or `git status --ignored`. Use a non-ignored path, update ignore rules, or `git add -f` only when force-adding is intentional. Do not retry the same ignored path unchanged."
	}
	if looksLikeSearchPathShellGlob(command) ||
		searchOutputLooksLikePathIOError(output) ||
		searchOutputLooksLikePathIOError(err.Error()) {
		return "Shell search path looks like an unexpanded glob or path IO error. Prefer toolkit `grep` with path + glob; if using shell rg, put filters in `-g \"*.go\"` and keep path as a real directory. Do not retry `rg pattern dir/**/*.go` on Windows."
	}
	if looksLikeSearchShellCommand(command) ||
		strings.Contains(combined, "regex parse") ||
		strings.Contains(combined, "regex error") {
		return "Shell search failed. Prefer toolkit `grep` (literal=true for fixed text, or pcre2=true). Fix path/pattern escaping; do not retry the identical shell rg/grep command."
	}
	if runtimeexecutor.IsWindows() && looksLikeBashHeredoc(command) {
		return "Windows shells do not support bash heredoc. Prefer dedicated file tools, `python -c`, or write a temp script with write/append_write then execute it."
	}
	if runtimeexecutor.IsWindows() &&
		(strings.Contains(combined, "the term 'head' is not recognized") ||
			strings.Contains(combined, "is not recognized as the name of a cmdlet") ||
			runtimeexecutor.HasPipedHeadToken(runtimeexecutor.SplitCommandTokens(command))) {
		return "Windows PowerShell/pwsh has no `head`. Use `Select-Object -First N`, or prefer toolkit view/grep/ls for inspection."
	}
	return ""
}

// looksLikeGitIgnoredPathFailure detects git operations that fail because the
// target path is excluded by ignore rules (common residual: git add/rm/update).
func looksLikeGitIgnoredPathFailure(command, combinedLower string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	if !strings.Contains(cmdLower, "git") {
		return false
	}
	switch {
	case strings.Contains(combinedLower, "the following paths are ignored by one of your .gitignore files"),
		strings.Contains(combinedLower, "ignored by one of your .gitignore"),
		strings.Contains(combinedLower, "is ignored by one of your .gitignore"),
		strings.Contains(combinedLower, "use -f if you really want to add them"),
		strings.Contains(combinedLower, "use -f if you really want to add it"),
		strings.Contains(combinedLower, "hint: use -f if you really want to add"),
		strings.Contains(combinedLower, "the following paths are ignored"),
		strings.Contains(combinedLower, "matches an ignore rule"):
		return true
	default:
		return false
	}
}

// bashCommandPreflight blocks known-invalid shell dialects before execution so
// models get actionable recovery instead of opaque parser errors.
func bashCommandPreflight(command string) (blocked bool, message, nextAction string) {
	if toolName, ok := looksLikeShellInvokedToolkitCommand(command); ok {
		return true,
			fmt.Sprintf("检测到在 shell 中调用 toolkit 命令风格参数（%s ...）。`%s`/`grep`/`ls`/`glob`/`view` 是专用工具，不是 shell 可执行文件。请直接调用 toolkit 工具，不要写成 `bash command=\"%s -path ...\"`。", toolName, toolName, toolName),
			fmt.Sprintf("Call the dedicated toolkit `%s` tool directly with structured args (path/file_path/pattern/glob). Do not invoke toolkit tool names as shell commands.", toolName)
	}
	// Path-position shell globs are especially harmful on Windows (os error 123),
	// but they are also a poor pattern on all shells versus -g / toolkit grep.
	if looksLikeSearchPathShellGlob(command) {
		return true,
			"检测到搜索命令在 path 位置使用了 shell 通配符（如 `backend/**/*.go` 或 `internal/errors/*.go`）。请改用 `rg -g \"*.go\" pattern backend`，或优先调用 toolkit `grep`（path + glob）。",
			"Prefer toolkit `grep` with path and glob. If shell rg is required, use `-g \"*.go\"` / `--glob` and keep path as a real directory; do not put `*` in the path argument."
	}
	if !runtimeexecutor.IsWindows() {
		return false, "", ""
	}
	if looksLikeBashHeredoc(command) {
		return true,
			"当前 Windows shell 不支持 bash heredoc 语法（<<EOF / <<'PY'）。请改用专用文件工具写入脚本，或使用 python -c / 单行命令。",
			"Prefer write/append_write + execute, python -c, or dedicated file/search tools. Do not retry bash heredoc on Windows."
	}
	return false, "", ""
}

// looksLikeSearchPathShellGlob reports rg/grep commands that place shell glob
// metacharacters in a path positional argument (after the pattern). Common
// residual failure on Windows: `rg -n foo backend/internal/**/*.go`.
func looksLikeSearchPathShellGlob(command string) bool {
	if !looksLikeSearchShellCommand(command) {
		return false
	}
	parts := runtimeexecutor.SplitCommandTokens(command)
	searchIdx := -1
	for i, part := range parts {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(part)))
		base = strings.TrimSuffix(base, ".exe")
		if isRipgrepOrGrepTool(base) {
			searchIdx = i
			break
		}
	}
	if searchIdx < 0 {
		return false
	}
	positional := 0
	for i := searchIdx + 1; i < len(parts); i++ {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		// Stop at common shell chain/pipe tokens; only inspect the search command.
		if part == "|" || part == "||" || part == "&&" || part == ";" {
			break
		}
		if strings.HasPrefix(part, "-") {
			if searchFlagConsumesNextValue(part) {
				// Combined forms like -g*.go / --glob=*.go already consumed the value.
				if searchFlagHasInlineValue(part) {
					continue
				}
				if i+1 < len(parts) && !strings.HasPrefix(strings.TrimSpace(parts[i+1]), "-") {
					i++
				}
			}
			continue
		}
		positional++
		// First positional is the pattern (regex may legally contain * ? []).
		if positional == 1 {
			continue
		}
		// Later positionals are paths: globs here are the residual failure mode.
		if pathTokenHasShellGlob(part) {
			return true
		}
	}
	return false
}

func searchFlagConsumesNextValue(flag string) bool {
	lower := strings.ToLower(strings.TrimSpace(flag))
	if searchFlagHasInlineValue(lower) {
		return true
	}
	switch lower {
	case "-e", "--regexp", "-f", "--file", "-g", "--glob", "--iglob",
		"-t", "--type", "-T", "--type-not", "-m", "--max-count",
		"-A", "--after-context", "-B", "--before-context", "-C", "--context",
		"--max-depth", "--max-filesize", "--sort", "--sortr",
		"-r", "--replace", "--type-add", "--type-clear", "--ignore-file",
		"--engine", "--field-context-separator", "--path-separator",
		"--context-separator":
		return true
	default:
		// Combined short form -g*.go already handled by HasInlineValue.
		return false
	}
}

func searchFlagHasInlineValue(flag string) bool {
	lower := strings.ToLower(strings.TrimSpace(flag))
	if strings.HasPrefix(lower, "--") {
		return strings.Contains(lower, "=")
	}
	// Short combined flags: -g*.go, -m10, -A2, -n is flag-only.
	if strings.HasPrefix(lower, "-g") && lower != "-g" {
		return true
	}
	if strings.HasPrefix(lower, "-e") && lower != "-e" && !strings.HasPrefix(lower, "-e-") {
		// -ePATTERN is uncommon but possible; treat non-exact -e* carefully.
		// Only -e followed by more alnum content without another short flag letter soup.
		return len(lower) > 2
	}
	return false
}

func pathTokenHasShellGlob(token string) bool {
	// Ignore escaped globs lightly: if every * is preceded by \, treat as literal.
	// Residual failures are almost always unescaped path globs.
	if !strings.ContainsAny(token, "*?[") {
		return false
	}
	// Require path-ish shape so pure weird tokens are less likely to false-positive.
	if strings.Contains(token, "/") ||
		strings.Contains(token, "\\") ||
		strings.Contains(token, ".") ||
		strings.Contains(token, ":") {
		return true
	}
	return strings.Contains(token, "*") || strings.Contains(token, "?")
}

// looksLikeShellInvokedToolkitCommand detects model mistakes like
// `view -path foo.go` or `grep -pattern X` executed via bash.
func looksLikeShellInvokedToolkitCommand(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", false
	}
	// Only intercept simple primary commands, not pipelines that legitimately
	// might include unrelated tokens after a real program.
	// Allow: "rg ... | Select-Object" (search tool). Block: bare toolkit names.
	cmdParts := runtimeexecutor.SplitCommandTokens(trimmed)
	if len(cmdParts) == 0 {
		return "", false
	}
	primary := strings.ToLower(filepath.Base(strings.TrimSpace(cmdParts[0])))
	primary = strings.TrimSuffix(primary, ".exe")
	switch primary {
	case "view", "ls", "glob", "grep", "write", "edit", "apply_patch", "append_write", "todos", "multiedit":
		// Require at least one toolkit-style flag/arg so we do not block a
		// hypothetical local binary named "view" with no args, and so that
		// "ls" alone can still be a shell command (Windows alias/dir).
		if primary == "ls" && !hasToolkitStyleFlag(cmdParts[1:]) {
			return "", false
		}
		if primary == "grep" {
			// Real system grep is common; only block when args look like toolkit schema.
			if !hasToolkitStyleFlag(cmdParts[1:]) {
				return "", false
			}
		}
		if primary == "view" || primary == "glob" || primary == "write" ||
			primary == "edit" || primary == "apply_patch" || primary == "append_write" ||
			primary == "todos" || primary == "multiedit" || hasToolkitStyleFlag(cmdParts[1:]) {
			return primary, true
		}
	}
	return "", false
}

func hasToolkitStyleFlag(parts []string) bool {
	for _, part := range parts {
		lower := strings.ToLower(strings.TrimSpace(part))
		switch {
		case strings.HasPrefix(lower, "-path"),
			strings.HasPrefix(lower, "--path"),
			strings.HasPrefix(lower, "-file_path"),
			strings.HasPrefix(lower, "--file_path"),
			strings.HasPrefix(lower, "-file-path"),
			strings.HasPrefix(lower, "--file-path"),
			strings.HasPrefix(lower, "-pattern"),
			strings.HasPrefix(lower, "--pattern"),
			strings.HasPrefix(lower, "-glob"),
			strings.HasPrefix(lower, "--glob"),
			strings.HasPrefix(lower, "-offset"),
			strings.HasPrefix(lower, "--offset"),
			strings.HasPrefix(lower, "-limit"),
			strings.HasPrefix(lower, "--limit"),
			strings.HasPrefix(lower, "-old_string"),
			strings.HasPrefix(lower, "--old_string"),
			strings.HasPrefix(lower, "-new_string"),
			strings.HasPrefix(lower, "--new_string"):
			return true
		}
	}
	return false
}

var bashHeredocPattern = regexp.MustCompile(`(<<[-]?\s*['"]?[A-Za-z_][A-Za-z0-9_]*['"]?)`)

func looksLikeBashHeredoc(command string) bool {
	if !strings.Contains(command, "<<") {
		return false
	}
	// Avoid false positives on PowerShell redirection like "2>&1" or comparison.
	// Require classic bash heredoc form: <<EOF / <<-EOF / <<'EOF' / <<"EOF".
	return bashHeredocPattern.MatchString(command)
}

// coerceBashCommandsParam accepts JSON-encoded commands arrays that models
// sometimes emit as strings under strict schemas.
func coerceBashCommandsParam(raw interface{}) (interface{}, bool, error) {
	text, ok := raw.(string)
	if !ok {
		return nil, false, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, nil
	}
	// Prefer JSON array/object decoding when the string looks structured.
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{") {
		var decoded interface{}
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, false, fmt.Errorf("commands 参数必须是对象数组；检测到 JSON 字符串但解析失败: %v", err)
		}
		switch decoded.(type) {
		case []interface{}, map[string]interface{}:
			// Single object becomes a one-item batch below via typed handling.
			if obj, isObj := decoded.(map[string]interface{}); isObj {
				return []interface{}{obj}, true, nil
			}
			return decoded, true, nil
		default:
			return nil, false, fmt.Errorf("commands 参数必须是对象数组（或 JSON 数组字符串）")
		}
	}
	// Bare command string is handled by the caller as a one-item batch.
	return nil, false, nil
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
