package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	stderrors "errors"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
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
	defaultShellCommandTimeout       = 30 * time.Second
	defaultGoTestCommandTimeout      = 5 * time.Minute
	defaultSearchShellCommandTimeout = 12 * time.Second
	shellCommandTimeoutEnv           = "AICLI_SHELL_COMMAND_TIMEOUT"
	shellCommandTimeoutMSEnv         = "AICLI_SHELL_COMMAND_TIMEOUT_MS"
	// shellTimeoutNoiseFloor rejects absurdly small numeric timeout_ms values.
	// Live sessions show models using timeout_ms=1/30 as if the unit were seconds;
	// deliberate sub-100ms budgets remain available through timeout="30ms".
	shellTimeoutNoiseFloor = 100 * time.Millisecond

	// Runtime-side ceiling on an explicitly supplied shell timeout. The default
	// is 0 = no ceiling: automation must be able to run a command to completion,
	// and the model's own timeout/timeout_ms argument is the only bound.
	// Operators can restore a hard cap via AICLI_SHELL_MAX_COMMAND_TIMEOUT
	// (see docs/tools/shell-command-guard.md).
	maxShellCommandTimeoutEnv     = "AICLI_SHELL_MAX_COMMAND_TIMEOUT"
	defaultMaxShellCommandTimeout = time.Duration(0)
)

// modelHistoryArtifactThresholdBytes keeps shell output artifacts aligned with
// the configurable model-visible tool text budget (default 12 KiB). Large
// outputs are persisted to the shell output artifact dir instead of entering
// history verbatim; output.SetModelToolTextByteBudget adjusts both sides.
func modelHistoryArtifactThresholdBytes() int {
	return output.ModelToolTextByteBudget()
}

// NewBashTool 创建 Bash 工具
func NewBashTool() *BashTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "单条 Shell 命令。需要顺序执行多条独立检查时改用 commands，减少 LLM 往返。用 workdir 切换目录；Windows PowerShell 没有 head 时使用 Select-Object。Windows 不要使用 bash heredoc（<<EOF）。",
			},
			"commands": map[string]interface{}{
				"type":        "array",
				"description": "命令批次（对象数组，也可容忍 JSON 字符串数组）。默认顺序执行；仅当各命令互不依赖且只读时可设置 parallel=true。每项可覆盖 workdir 与 timeout_sec/timeout_ms。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command":     map[string]interface{}{"type": "string"},
						"workdir":     map[string]interface{}{"type": "string"},
						"timeout_ms":  map[string]interface{}{"type": "integer", "minimum": 1, "description": "毫秒超时；小于 100 的数值会按模型占位噪声忽略。"},
						"timeout_sec": map[string]interface{}{"type": "integer", "minimum": 1, "description": "秒级超时；普通命令默认 30s，测试/构建等长命令请显式设置。"},
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
			// 超时只对模型暴露整数字段：2026-09-27 证据显示 65/66 条
			// invalid_tool_arguments 是字符串型超时被写成裸值（"timeout": 60s），
			// 而裸数字 60 是合法 JSON，模型很难把它写坏。
			// 执行端仍兼容旧的 timeout="2m"/"30ms" 字符串形式（见
			// resolveShellCommandTimeout），以兼容历史会话与其它客户端调用，
			// 但不再把它放进模型可见的工具面。
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时毫秒数。小于 100 的数值会视为模型单位混淆并忽略。",
			},
			"timeout_sec": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时秒数（正整数，推荐）。普通命令默认 30s；go test 未显式设置时自动使用至少 5m；shell 代码搜索（rg/grep/findstr）未显式设置时默认更短（约 12s）以促使改用 toolkit grep；环境变量和显式参数仍可覆盖。优先级低于 timeout_ms，与 timeout_ms 二选一即可。",
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
			"detach": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时把命令作为独立进程启动（fire-and-forget），立即返回 PID。该进程不继承输出管道、不纳入本次调用的进程树守卫（不受超时/Esc 终止影响），适用于需要真实控制台/TTY 的交互程序（如再开一个 aicli TUI）或长期守护进程。输出不会被捕获；仅支持单条 command（不适用于 commands 批次）；运行时可用 AICLI_SHELL_ALLOW_DETACH=0 全局禁用。",
			},
		},
		"required": []string{},
	}

	return &BashTool{
		BaseTool: toolkit.NewBaseTool(
			"bash",
			"执行一条 Shell 命令，或用 commands 顺序执行一批检查并一次返回全部结果。仅当后续命令依赖前一条输出且需模型决策时才拆分。进程非零退出码会作为内容结果返回（含 Exit code/Output），不是工具崩溃；仅未启动/超时/取消/权限拒绝等才是硬失败。Windows 默认 PowerShell，没有 head 时用 Select-Object；不要使用 bash heredoc（<<EOF）。",
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
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindExec,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
		// bash / execute_shell_command are compatibility aliases: the canonical
		// model-facing shell surface is `shell` (agent-side dedupe keeps one).
		"alias_of": "shell",
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
	// Process-tree guard diagnostics: whether the runtime had to terminate the
	// whole tree, which PIDs were killed, and whether WaitDelay had to bound
	// post-exit I/O (descendants keeping the output pipe open).
	ProcessTreeKill     bool
	KilledPIDs          []int
	ProcessTreeMode     string
	ProcessTreeError    string
	WaitDelayMs         int64
	WaitDelayUsed       bool
	Termination         string
	LeftoverDescendants []int
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
//
// Every return path (single command, batch, hard failure) passes through
// ownShellOutputWindow: the shell owns its model-visible window, archives the
// complete capture before folding it, and stamps skip_render_truncation so the
// render layer never folds shell output a second time.
func (b *BashTool) Execute(ctx context.Context, params map[string]interface{}) (result *toolkit.ToolResult, err error) {
	defer func() { result = ownShellOutputWindow(ctx, "bash", result) }()

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
	if searchTool, redirect := looksLikeSimpleShellCodeSearch(command); redirect {
		nextAction := "Call toolkit `grep` directly with structured pattern/path/glob arguments; use literal=true for fixed text. Do not replay the same shell search."
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"未执行简单 shell `%s` 代码搜索；runtime 已将其软重定向到专用 toolkit `grep`。这不是无匹配结果，也不是 hard failure。请按 next_action 直接调用 grep。",
				searchTool,
			),
			Metadata: map[string]interface{}{
				toolresult.MetadataOutcomeKey:    toolresult.OutcomeSuccess,
				toolresult.MetadataNextActionKey: nextAction,
				"shell_search_redirected":        true,
				"redirect_tool":                  "grep",
				"executed":                       false,
			},
		}, nil
	}
	mutatedPaths := extractStringList(params["mutated_paths"])
	workdir := extractString(params["workdir"])
	// Report and hint against the directory the command will actually run in
	// (the session workspace root when the run context carries one) instead of
	// the raw argument: an omitted workdir used to leave the model-facing
	// "Workdir:" line blank and anchored relative-path hints to the server
	// process directory rather than the bound workspace.
	effectiveWorkdir, workdirErr := b.workdirForExecution(ctx, workdir)
	if workdirErr != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      workdirErr,
		}, nil
	}
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

	// Path-position globs only fail when the literal token reaches rg/grep:
	// an unmatched glob, a quoted token, or a shell that never expands globs
	// (PowerShell/cmd). A glob that really expands under effectiveWorkdir -
	// e.g. `grep -rn x cmd/aicli/commands/*.go` on bash - is a legitimate
	// search and runs normally. A literal-reaching glob is soft-redirected
	// (executed=false + next_action) instead of hard-failed, so piped/compound
	// searches share the simple-search redirect contract and a fixable command
	// shape does not poison batch accounting.
	if globToken, literal := searchPathGlobLiteralToken(command, effectiveWorkdir); literal {
		return bashSearchPathGlobRedirectResult(globToken), nil
	}

	// detach=true: fire-and-forget launch outside the per-command job object.
	// Opt-in per call, and refusable through AICLI_SHELL_ALLOW_DETACH=0.
	if detachRequested, _ := resolveBoolParam(params, "detach"); detachRequested {
		return b.executeDetachedCommand(ctx, command, effectiveWorkdir)
	}

	// 使用 executer 执行命令
	started := time.Now()
	execResult, err := b.executeCommand(ctx, command, effectiveWorkdir, timeout, captureSettings)
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
			} else if hint := friendlyHintFor(command, execResult.Output, err, effectiveWorkdir); hint != "" {
				content = strings.TrimSpace(content + "\n" + hint)
			}
			content = formatShellCommandContent(1, execResult.ShellType, effectiveWorkdir, duration, false, content)
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
			annotateTerminationMetadata(metadata, err)
			if duration > 0 {
				metadata["duration_ms"] = duration.Milliseconds()
			}
			if next := bashCommandFailureNextAction(command, execResult.Output, err, effectiveWorkdir); next != "" {
				metadata[toolresult.MetadataNextActionKey] = next
			}
			content := formatShellCommandContent(exitCode, execResult.ShellType, effectiveWorkdir, duration, false, execResult.Output)
			if hint := friendlyHintFor(command, execResult.Output, err, effectiveWorkdir); hint != "" {
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
		annotateTerminationMetadata(failureMetadata, err)
		if next := bashCommandFailureNextAction(command, execResult.Output, err, effectiveWorkdir); next != "" {
			failureMetadata[toolresult.MetadataNextActionKey] = next
		}
		recoveryHint := ""
		if isShellTimeoutError(err) {
			recoveryHint = longRunningShellCommandHint(command)
			if recoveryHint != "" {
				failureMetadata["long_running_command_hint"] = true
			}
		}
		if recoveryHint == "" && execResult.WaitDelayUsed {
			recoveryHint = "命令本体已结束，但仍有后代进程占用输出管道；runtime 已按 WaitDelay 收敛，输出可能不完整。若该进程是有意保留的守护进程，请改用 background_task，或在命令内显式等待/终止该子进程。"
			failureMetadata["wait_delay_note"] = true
		}
		if recoveryHint != "" {
			if existing, _ := failureMetadata[toolresult.MetadataNextActionKey].(string); strings.TrimSpace(existing) == "" {
				failureMetadata[toolresult.MetadataNextActionKey] = recoveryHint
			}
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
		if recoveryHint != "" {
			result.Content = strings.TrimRight(result.Content, "\n") + "\n" + recoveryHint
		}
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
		Content:    formatShellCommandContent(0, execResult.ShellType, effectiveWorkdir, duration, false, execResult.Output),
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
	if detachRequested, _ := resolveBoolParam(parent, "detach"); detachRequested {
		return toolResultFailureWithCode(
			fmt.Errorf("detach=true 只支持单条 command 调用，不支持 commands 批次；请拆分为多次 bash 调用"),
			string(runtimeerrors.ErrToolExecution),
			"Call bash once per detached launch with a single `command` and detach=true.",
			map[string]interface{}{"detach_batch_refused": true},
		), nil
	}
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
	if detach, _ := resolveBoolParam(commandParams, "detach"); detach {
		return toolResultFailureWithCode(
			fmt.Errorf("detach=true 只支持单条 command 调用，不支持 commands 批次；请拆分为多次 bash 调用"),
			string(runtimeerrors.ErrToolExecution),
			"Call bash once per detached launch with a single `command` and detach=true.",
			map[string]interface{}{"detach_batch_refused": true},
		)
	}
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
	// Batch output declares the shell's own model-visible window (see
	// buildCommandExecutionMetadata). Execute folds the batch body to that window
	// and stamps the render-layer opt-out before the result leaves the tool, so
	// this number describes the window the model really gets.
	metadata[toolresult.MetadataModelVisibleBudgetKey] = shellOutputBudgetBytes
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
	if len(batchOutput) > modelHistoryArtifactThresholdBytes() {
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
	resolvedWorkdir, err := b.workdirForExecution(ctx, workdir)
	if err != nil {
		return CommandExecutionResult{}, err
	}
	budget := resolveShellTimeoutBudget(ctx, timeout)

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
	// budget.Effective == 0 表示不设超时（默认关闭上限且调用方未指定）。
	// context.WithTimeout(ctx, 0) 会立刻过期并取消命令，因此必须显式跳过。
	cmdCtx := ctx
	if budget.Effective > 0 {
		withTimeout, cancel := context.WithTimeout(ctx, budget.Effective)
		defer cancel()
		cmdCtx = withTimeout
	}

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

	cmd := exec.Command(shellCmd[0], shellCmd[1:]...)
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
		cmd = exec.Command(launch.Command, launch.Args...)
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

	outputMirror := resolveToolTerminalOutputMirror(ctx)
	if outputMirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}
	artifactRoot := toolctx.ShellOutputArtifactDir(ctx)
	guard := runtimeexecutor.NewProcessGuard()
	if bindErr := guard.Bind(cmd); bindErr != nil {
		guard.Close()
		return CommandExecutionResult{}, bindErr
	}
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputGuarded(cmdCtx, cmd, guard, runtimeexecutor.GuardedCaptureOptions{
		MaxBytes:      captureSettings.captureLimitBytes(),
		Scope:         "toolkit",
		Command:       command,
		PreferredRoot: artifactRoot,
		Mirror:        outputMirror,
	})
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command, artifactRoot)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			result = applyProcessGuardResult(result, guard, err, "timeout")
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
			result = applyProcessGuardResult(result, guard, err, "cancel")
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.ContextCancellationError(ctx)
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		result = applyCommandExecutionShell(result, shell)
		result = applyProcessGuardResult(result, guard, err, "error")
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}
	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	result = applyCommandExecutionShell(result, shell)
	result = applyProcessGuardResult(result, guard, err, "")
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

	budget := resolveShellTimeoutBudget(ctx, timeout)
	// 同上：Effective == 0 表示不设超时，不能用 WithTimeout(0) 立即取消命令。
	cmdCtx := ctx
	if budget.Effective > 0 {
		withTimeout, cancel := context.WithTimeout(ctx, budget.Effective)
		defer cancel()
		cmdCtx = withTimeout
	}

	// 使用智能 shell 检测
	shell := runtimeexecutor.DefaultUserShell()
	shellArgs := shell.DeriveExecArgs(command, false)

	cmd := exec.Command(shellArgs[0], shellArgs[1:]...)

	// 设置工作目录
	if cfg.workdir != "" {
		cmd.Dir = cfg.workdir
	}

	// 过滤敏感环境变量，并注入 resolver 选定的 rg 目录，保证 shell 内可直接调用 rg。
	cmd.Env = runtimeexecutor.WithResolvedRipgrepPath(runtimeexecutor.FilterSensitiveEnv(os.Environ()))

	// PowerShell 需要 UTF-8 输出编码
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8(cmd)
	}

	outputMirror := resolveToolTerminalOutputMirror(ctx)
	if outputMirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}

	artifactRoot := toolctx.ShellOutputArtifactDir(ctx)
	guard := runtimeexecutor.NewProcessGuard()
	if bindErr := guard.Bind(cmd); bindErr != nil {
		guard.Close()
		return CommandExecutionResult{}, bindErr
	}
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputGuarded(cmdCtx, cmd, guard, runtimeexecutor.GuardedCaptureOptions{
		MaxBytes:      captureLimitBytesFromExecConfig(cfg),
		Scope:         "toolkit",
		Command:       command,
		PreferredRoot: artifactRoot,
		Mirror:        outputMirror,
	})
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command, artifactRoot)

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			result = applyProcessGuardResult(result, guard, err, "timeout")
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.TimeoutError(budget)
		}
		if cmdCtx.Err() == context.Canceled {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			result = applyCommandExecutionShell(result, shell)
			result = applyProcessGuardResult(result, guard, err, "cancel")
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
			result = applyProcessGuardResult(result, guard, err, "error")
			applyCommandTimeoutBudget(&result, budget)
			return result, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, GetShellEnvironmentInfo())
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		result = applyCommandExecutionShell(result, shell)
		result = applyProcessGuardResult(result, guard, err, "error")
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}

	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	result = applyCommandExecutionShell(result, shell)
	result = applyProcessGuardResult(result, guard, err, "")
	applyCommandTimeoutBudget(&result, budget)
	return result, nil
}

// --- Helper functions ---

// resolveToolTerminalOutputMirror tees the chat OutputMirror (if any) with a
// tool.progress terminal stream writer when a progress Reporter is bound.
// Consumers on the bus (API SSE / ACP / non-interactive) then see live chunks;
// interactive chat keeps its existing direct terminal mirror.
func resolveToolTerminalOutputMirror(ctx context.Context) io.Writer {
	existing := runtimeexecutor.OutputMirrorFromContext(ctx)
	mirror, _ := toolprotocol.TeeOutputMirrorWithTerminalStream(ctx, existing, toolprotocol.TerminalStreamOptions{
		Channel: toolprotocol.StreamChannelCombined,
		Message: "stdout",
	})
	return mirror
}

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

// resolveShellTimeoutBudget applies the runtime-side ceiling on top of the
// requested timeout. The ceiling is disabled by default (0 = unlimited) and is
// only applied when AICLI_SHELL_MAX_COMMAND_TIMEOUT opts into a hard cap.
func resolveShellTimeoutBudget(ctx context.Context, requested time.Duration) runtimeexecution.TimeoutBudget {
	budget := runtimeexecution.ResolveTimeout(ctx, requested)
	return runtimeexecution.LimitTimeout(budget, resolveMaxShellCommandTimeout(), runtimeexecution.TimeoutSourceRuntimeCeiling)
}

// resolveMaxShellCommandTimeout returns the configured hard cap, or 0 when the
// ceiling is disabled (the default). off/0/disable also disable it, and an
// unparsable value falls back to the default (disabled) instead of silently
// imposing a cap.
func resolveMaxShellCommandTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(maxShellCommandTimeoutEnv))
	if raw == "" {
		return defaultMaxShellCommandTimeout
	}
	switch strings.ToLower(raw) {
	case "off", "0", "false", "no", "disable", "disabled":
		return 0
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	return defaultMaxShellCommandTimeout
}

// executeDetachedCommand launches the command as an independent process
// (detach=true): new console/session, null stdio, no job-object assignment and
// no output capture. It returns immediately with the PID so the caller can
// manage the process explicitly.
func (b *BashTool) executeDetachedCommand(ctx context.Context, command, resolvedWorkdir string) (*toolkit.ToolResult, error) {
	if !runtimeexecutor.ResolveDetachAllowed() {
		return toolResultFailureWithCode(
			fmt.Errorf("detach=true 已被运行时禁用（%s=0）", runtimeexecutor.DetachAllowedEnv),
			string(runtimeerrors.ErrAgentPermission),
			"Drop detach=true, or ask the operator to unset "+runtimeexecutor.DetachAllowedEnv+".",
			map[string]interface{}{
				"detach_refused": true,
				"detach_gate":    runtimeexecutor.DetachAllowedEnv,
			},
		), nil
	}

	shell := runtimeexecutor.DefaultUserShell()
	shellCmd := shell.DeriveExecArgs(command, false)
	if len(shellCmd) == 0 {
		return toolResultFailureWithCode(
			fmt.Errorf("detach: 无法解析 shell 启动命令"),
			string(runtimeerrors.ErrProcessStartFailed),
			"",
			map[string]interface{}{"detach_failed": true},
		), nil
	}
	if err := b.sandbox.CheckCommandDenied(shellCmd[0]); err != nil {
		return toolResultFailureWithCode(
			wrapSandboxPermissionError("sandbox denied detached shell launcher", err, map[string]interface{}{
				"policy":    "sandbox",
				"operation": string(runtimeexecutor.OpExecute),
				"command":   command,
				"launcher":  shellCmd[0],
			}),
			string(runtimeerrors.ErrAgentPermission),
			"",
			map[string]interface{}{"detach_refused": true},
		), nil
	}

	launchPath := shellCmd[0]
	launchArgs := shellCmd[1:]
	launchDir := resolvedWorkdir
	launchEnv := runtimeexecutor.BuildFilteredEnv(b.sandbox, os.Environ())
	if launch, wrapErr := b.sandbox.PrepareOSCommand(ctx, shellCmd[0], shellCmd[1:], resolvedWorkdir, launchEnv); wrapErr != nil {
		return toolResultFailureWithCode(
			wrapSandboxPermissionError("sandbox denied detached os isolation", wrapErr, map[string]interface{}{
				"policy":    "sandbox",
				"operation": string(runtimeexecutor.OpExecute),
				"command":   command,
				"launcher":  shellCmd[0],
				"os_mode":   b.sandbox.Config().OSSandbox,
			}),
			string(runtimeerrors.ErrAgentPermission),
			"",
			map[string]interface{}{"detach_refused": true},
		), nil
	} else if strings.TrimSpace(launch.Command) != "" {
		launchPath = launch.Command
		launchArgs = launch.Args
		if strings.TrimSpace(launch.WorkDir) != "" {
			launchDir = launch.WorkDir
		}
		if launch.Env != nil {
			launchEnv = launch.Env
		}
	}

	started := time.Now()
	proc, err := runtimeexecutor.StartDetached(runtimeexecutor.DetachedLaunch{
		Path: launchPath,
		Args: launchArgs,
		Dir:  launchDir,
		Env:  launchEnv,
	})
	duration := time.Since(started)
	if err != nil {
		return toolResultFailureWithCode(
			err,
			string(runtimeerrors.ErrProcessStartFailed),
			"Fix the executable/workdir, or run the command in the foreground to surface its output.",
			map[string]interface{}{"detach_failed": true},
		), nil
	}

	content := fmt.Sprintf(
		"已在独立控制台启动（detach=true，fire-and-forget）。\nPID: %d\n隔离模式: %s（breakaway=%v）\n命令: %s\n说明: 该进程不继承输出管道，输出不会进入本工具结果；它不受本次调用超时/Esc 终止影响，请用 PID 显式管理（Windows: `Stop-Process -Id %d -Force`；Unix: `kill %d`）。",
		proc.PID, proc.Mode, proc.Breakaway, command, proc.PID, proc.PID,
	)
	if warning := strings.TrimSpace(proc.Warning); warning != "" {
		content += "\n提示: " + warning
		content += "（该进程仍独立于本次调用的作业对象，但可能被外层作业跟踪。）"
	}
	metadata := map[string]interface{}{
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"detached":                    true,
		"detached_pid":                proc.PID,
		"detach_mode":                 proc.Mode,
		"detach_breakaway":            proc.Breakaway,
		"executed":                    true,
		"duration_ms":                 duration.Milliseconds(),
		toolresult.MetadataNextActionKey: fmt.Sprintf(
			"Detached process started (pid=%d); it is absent from this tool's output and leftover tracking. Manage it explicitly (Stop-Process -Id %d / kill %d) and avoid launching duplicates.",
			proc.PID, proc.PID, proc.PID,
		),
	}
	if warning := strings.TrimSpace(proc.Warning); warning != "" {
		metadata["detach_warning"] = warning
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata:   metadata,
	}, nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// applyProcessGuardResult copies process-tree termination diagnostics onto a
// command result so callers (and the model) can see that the runtime had to
// terminate the tree or bound the post-exit wait instead of hanging.
func applyProcessGuardResult(result CommandExecutionResult, guard *runtimeexecutor.ProcessGuard, runErr error, termination string) CommandExecutionResult {
	if guard == nil {
		return result
	}
	report := guard.Report()
	result.ProcessTreeKill = report.TreeKill
	result.KilledPIDs = report.Killed
	result.ProcessTreeMode = report.Mode
	result.ProcessTreeError = strings.TrimSpace(strings.Join(nonEmptyStrings(report.AttachErr, report.Err), "; "))
	result.WaitDelayMs = guard.WaitDelay().Milliseconds()
	if stderrors.Is(runErr, exec.ErrWaitDelay) {
		result.WaitDelayUsed = true
	}
	if len(report.Leftovers) > 0 {
		result.LeftoverDescendants = report.Leftovers
	}
	if strings.TrimSpace(termination) != "" {
		result.Termination = termination
	}
	return result
}

var longRunningShellCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bdaemon\s+(start|stop|restart|status)\b`),
	regexp.MustCompile(`(?i)\bstart-process\b`),
	regexp.MustCompile(`(?i)\b(npm|pnpm|yarn|bun)\s+(run\s+)?(dev|serve|start)\b`),
	regexp.MustCompile(`(?i)\bdocker[\s-]?compose\s+up\b`),
	regexp.MustCompile(`(?i)\b(docker|podman)\s+run\b`),
	regexp.MustCompile(`(?i)\b(tail\s+-f|journalctl\s+-f)\b`),
	regexp.MustCompile(`(?i)\bssh\s+\S+`),
	regexp.MustCompile(`(?i)\b(http-server|vite|webpack-dev-server)\b`),
}

// longRunningShellCommandHint returns a recovery hint for commands that look
// like long-lived processes (daemons, dev servers, watchers). Such commands
// should carry an explicit timeout or run through the background task tool.
func longRunningShellCommandHint(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	for _, pattern := range longRunningShellCommandPatterns {
		if pattern.MatchString(trimmed) {
			return "该命令疑似常驻/守护类命令（daemon、dev server、watch 等）：前台执行请显式传 timeout，并优先改用 background_task；若必须先拉起守护进程，请先 `daemon start`（带超时）再查询状态，避免首次调用阻塞。"
		}
	}
	return ""
}

func isShellTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) || runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded)
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
	// Shell output owns its model-visible window instead of relying on the
	// render-layer backstop: the capture limit (256 KiB default) bounds memory,
	// and ownShellOutputWindow folds the body to shellOutputBudgetBytes as a
	// head+tail window after archiving the complete capture, so the omitted
	// middle stays pageable with artifact_read while errors and test verdicts
	// stay visible in the tail. The budget is declared here as well because the
	// window is a property of the result, not of the fold path.
	metadata[toolresult.MetadataModelVisibleBudgetKey] = shellOutputBudgetBytes
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
	if result.WaitDelayMs > 0 {
		metadata["wait_delay_ms"] = result.WaitDelayMs
	}
	if result.WaitDelayUsed {
		metadata["wait_delay_used"] = true
	}
	if result.ProcessTreeKill {
		metadata["process_tree_kill"] = true
	}
	if strings.TrimSpace(result.ProcessTreeMode) != "" {
		metadata["process_tree_mode"] = strings.TrimSpace(result.ProcessTreeMode)
	}
	if strings.TrimSpace(result.ProcessTreeError) != "" {
		metadata["process_tree_error"] = strings.TrimSpace(result.ProcessTreeError)
	}
	if len(result.KilledPIDs) > 0 {
		metadata["killed_pids"] = result.KilledPIDs
	}
	if len(result.LeftoverDescendants) > 0 {
		metadata["leftover_descendant_pids"] = result.LeftoverDescendants
	}
	if strings.TrimSpace(result.Termination) != "" {
		metadata["termination"] = strings.TrimSpace(result.Termination)
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
	if capture.TotalBytes <= modelHistoryArtifactThresholdBytes() || strings.TrimSpace(capture.Output) == "" {
		return artifactPath, artifactErr
	}
	path, err := runtimeexecutor.PersistShellOutputArtifact(scope, command, preferredRoot, capture.Output)
	if err != nil {
		return "", err
	}
	// O-1: shell-disk artifact landed; count it in the archive mix.
	observability.RecordToolOutputArchive(observability.ArchiveLayerShellDisk, observability.ArchiveDispositionArchived)
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
	// -EncodedCommand already carries the UTF-8 directive inside its base64
	// payload; prefixing the argument would corrupt the encoded script.
	if runtimeexecutor.ShellArgsUseEncodedCommand(cmd.Args) {
		return
	}
	// The command is the last arg; prepend UTF-8 encoding directive
	lastIdx := len(cmd.Args) - 1
	cmd.Args[lastIdx] = runtimeexecutor.PowerShellCommandPrefix + cmd.Args[lastIdx]
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

	exitCode := exitCodeFromError(err)

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
		searchPathGlobReachesTool(command, workdir) ||
		searchOutputLooksLikePathIOError(output):
		if hint := friendlyHintForSearchTool(searchCmd, command, output, exitCode, workdir); hint != "" {
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
		if isRipgrepOrGrepTool(base) || isPrimaryOnlySearchTool(base) {
			return base
		}
	}
	return ""
}

func firstRipgrepOrGrepToken(cmdParts []string) string {
	for _, part := range cmdParts {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(part)))
		base = strings.TrimSuffix(base, ".exe")
		if isRipgrepOrGrepTool(base) {
			return base
		}
	}
	return ""
}

// primaryCommandBase returns the first non-assignment command token base name
// (e.g. rg, select-string), skipping simple FOO=bar env prefixes.
func primaryCommandBase(cmdParts []string) string {
	for _, part := range cmdParts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		// Skip simple env assignments: FOO=bar
		if eq := strings.IndexByte(trimmed, '='); eq > 0 &&
			!strings.ContainsAny(trimmed[:eq], `/\`) &&
			!strings.HasPrefix(trimmed, "-") {
			continue
		}
		base := strings.ToLower(filepath.Base(trimmed))
		return strings.TrimSuffix(base, ".exe")
	}
	return ""
}

// isPrimaryOnlySearchTool reports search tools whose exit-1 no-match semantics
// must only apply when they are the primary command (not a filter after builds).
func isPrimaryOnlySearchTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "select-string", "findstr":
		return true
	default:
		return false
	}
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
func friendlyHintForSearchTool(toolName, command, output string, exitCode int, workdir string) string {
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
	case searchPathGlobReachesTool(command, workdir) ||
		searchOutputLooksLikePathIOError(output) ||
		searchOutputLooksLikePathIOError(cleaned):
		return fmt.Sprintf(
			"提示: %s 路径参数含 shell 通配符或路径 IO 失败（Windows 常见 os error 123 / 文件名语法不正确）。不要写 `rg pattern backend/**/*.go`；改用 `rg -g \"*.go\" pattern backend`，或优先 toolkit `grep` 的 path/glob。命令: %s",
			toolName,
			truncateDiagnosticText(strings.TrimSpace(command), 160),
		)
	case looksLikeMissingSearchToolOutput(cleanedLower, toolName) ||
		looksLikeMissingSearchToolOutput(rawLower, toolName):
		// The shell could not find rg/grep itself. Steer to toolkit grep, whose
		// builtin scanner makes ripgrep optional, instead of retrying an
		// install-dependent command.
		return fmt.Sprintf(
			"提示: 当前 shell 找不到 `%s`（未安装或不在 PATH）。直接改用 toolkit `grep`：它优先使用 rg，不可用时自动回退内置扫描，无需安装 ripgrep；确需 shell rg 时可安装 ripgrep，或设置 AICLI_RG_PATH 后以该绝对路径调用。",
			toolName,
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

// looksLikeMissingSearchToolOutput reports shell output where the search tool
// binary itself is missing, while avoiding blaming rg for a missing pipe stage
// such as `head`. The caller passes lowercased text and the tool name.
func looksLikeMissingSearchToolOutput(outputLower, toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" || strings.TrimSpace(outputLower) == "" {
		return false
	}
	for _, quoted := range []string{"'" + name + "'", "\"" + name + "\""} {
		if strings.Contains(outputLower, quoted) &&
			(strings.Contains(outputLower, "not recognized") ||
				strings.Contains(outputLower, "command not found") ||
				strings.Contains(outputLower, "executable file not found") ||
				strings.Contains(outputLower, "not found")) {
			return true
		}
	}
	switch {
	case strings.Contains(outputLower, name+": command not found"),
		strings.Contains(outputLower, name+": not found"),
		strings.Contains(outputLower, "exec: \""+name+"\""),
		strings.Contains(outputLower, "exec: "+name+":"):
		return true
	}
	return false
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
	} else if isPrimaryOnlySearchTool(searchCmd) {
		// Select-String / findstr only soft-empty when they are the primary
		// command. Pipelines like `tsc | Select-String` must not hide real
		// build failures behind empty-search success.
		if primaryCommandBase(cmdParts) != searchCmd {
			// Still allow soft-empty when an rg/grep stage is present in the pipe.
			if firstRipgrepOrGrepToken(cmdParts) == "" {
				return false
			}
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
//
// Signal deaths are reported the way shells report them: a process killed by
// signal N has no exit status of its own (Go's ExitCode() returns -1), so the
// observable code is 128+N (SIGKILL → 137, SIGTERM → 143). Reporting -1 would
// make the model read "killed by the OS" as "unknown tool failure".
func exitCodeFromError(err error) int {
	if err == nil {
		return -1
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
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

// terminationFromError classifies how a failed process ended: a signal death
// returns termination="signal" plus the signal label (e.g. "killed"), anything
// else returns empty strings so callers keep whatever termination the process
// guard already recorded (timeout, tree kill, ...).
func terminationFromError(err error) (termination string, signalName string) {
	var exitErr *exec.ExitError
	if !stderrors.As(err, &exitErr) {
		return "", ""
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return "", ""
	}
	return "signal", status.Signal().String()
}

// annotateTerminationMetadata stamps the signal-death classification onto a
// result's metadata: the guard's own termination reason (e.g. timeout) wins
// when present, because "we killed it after the timeout" is more actionable
// than the bare signal that reached the process. The generic "error" placeholder
// that applyProcessGuardResult stamps on ordinary failures is not a reason and
// must not mask a specific signal classification.
func annotateTerminationMetadata(metadata map[string]interface{}, err error) {
	if metadata == nil {
		return
	}
	termination, signalName := terminationFromError(err)
	if termination == "" {
		return
	}
	if existing, _ := metadata["termination"].(string); isGenericTerminationPlaceholder(existing) {
		metadata["termination"] = termination
	}
	if signalName != "" {
		metadata["signal"] = signalName
	}
}

// isGenericTerminationPlaceholder 报告 termination 值是否只是占位而没有给出
// 具体原因：空串，或 applyProcessGuardResult 在普通失败路径写入的 "error"。
// timeout/cancel 等具体原因不在此列，它们优先于信号分类保留。
func isGenericTerminationPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || trimmed == "error"
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

	var (
		hasMS, hasSec, hasNamed bool
		ms, sec, named          time.Duration
	)

	if raw, ok := params["timeout_ms"]; ok && raw != nil && !isNumericZero(raw) {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_ms 参数无效: %w", err)
		}
		ms = time.Duration(value) * time.Millisecond
		hasMS = true
	}
	if raw, ok := params["timeout_sec"]; ok && raw != nil && !isNumericZero(raw) {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_sec 参数无效: %w", err)
		}
		sec = time.Duration(value) * time.Second
		hasSec = true
	}
	if raw, ok := params["timeout"]; ok && raw != nil {
		timeoutText, ok := raw.(string)
		if !ok {
			// Non-string timeout is treated as absent; fall through to other keys/default.
		} else {
			timeoutText = strings.TrimSpace(timeoutText)
			if timeoutText != "" {
				if seconds, numberErr := strconv.ParseFloat(timeoutText, 64); numberErr == nil && seconds > 0 {
					named = time.Duration(seconds * float64(time.Second))
					hasNamed = true
				} else {
					parsed, err := time.ParseDuration(timeoutText)
					if err != nil || parsed <= 0 {
						return 0, fmt.Errorf("timeout 参数无效: %q", timeoutText)
					}
					named = parsed
					hasNamed = true
				}
			}
		}
	}

	// Prefer coarser seconds/duration when present; otherwise treat a sub-floor
	// numeric millisecond value as schema noise and use the inferred/default
	// timeout. This prevents 1ms/30ms hard failures caused by unit confusion.
	if hasMS && ms < shellTimeoutNoiseFloor {
		if hasSec && sec >= time.Second && sec > ms {
			hasMS = false
		} else if hasNamed && named >= time.Second && named > ms {
			hasMS = false
		} else {
			hasMS = false
		}
	}

	if hasMS {
		return ms, nil
	}
	if hasSec {
		return sec, nil
	}
	if hasNamed {
		return named, nil
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
			if key == "timeout_ms" {
				if milliseconds, err := extractPositiveInt(value); err == nil && time.Duration(milliseconds)*time.Millisecond < shellTimeoutNoiseFloor {
					continue
				}
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
	if firstRipgrepOrGrepToken(cmdParts) != "" {
		return true
	}
	if isPrimaryOnlySearchTool(primaryCommandBase(cmdParts)) {
		return true
	}
	lower := strings.ToLower(command)
	return strings.Contains(lower, "rg ") ||
		strings.Contains(lower, "rg.exe") ||
		strings.HasPrefix(strings.TrimSpace(lower), "rg") ||
		strings.Contains(lower, "grep ")
}

// looksLikeSimpleShellCodeSearch recognizes standalone rg invocations that can
// be expressed by the dedicated toolkit grep tool. It intentionally excludes
// pipelines/chains/redirections and administrative rg modes so shell remains
// available when it provides behavior the toolkit grep API does not.
func looksLikeSimpleShellCodeSearch(command string) (string, bool) {
	parts := runtimeexecutor.SplitCommandTokens(command)
	if len(parts) < 2 {
		return "", false
	}
	primary := primaryCommandBase(parts)
	if primary != "rg" && primary != "ripgrep" {
		return "", false
	}
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "|", "||", "&&", "&", ";", ">", "<":
			return "", false
		}
	}
	for _, part := range parts[1:] {
		lower := strings.ToLower(strings.TrimSpace(part))
		if eq := strings.IndexByte(lower, '='); eq >= 0 {
			lower = lower[:eq]
		}
		switch lower {
		case "-h", "--help", "-v", "--version", "--pcre2-version",
			"--files", "--files-with-matches", "--files-without-match",
			"--type-list", "--type-add", "--type-clear", "--generate":
			return "", false
		}
	}
	return primary, true
}

func bashSearchNoMatchNextAction() string {
	return "Treat this as no-match / empty evidence, not a hard crash. Prefer toolkit `grep` (literal/paths/glob) for code search; change pattern or broaden path/workdir. Do not retry the identical shell query unchanged."
}

// bashCommandFailureNextAction returns structured recovery guidance for hard
// shell failures. Search-like failures prefer the dedicated grep tool.
func bashCommandFailureNextAction(command, output string, err error, workdir string) string {
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
	if searchPathGlobReachesTool(command, workdir) ||
		searchOutputLooksLikePathIOError(output) ||
		searchOutputLooksLikePathIOError(err.Error()) {
		return "Shell search path looks like an unexpanded glob or path IO error. Prefer toolkit `grep` with path + glob; if using shell rg, put filters in `-g \"*.go\"` and keep path as a real directory. Do not retry `rg pattern dir/**/*.go` on Windows."
	}
	if looksLikeMissingSearchToolOutput(combined, firstSearchToolToken(runtimeexecutor.SplitCommandTokens(command))) {
		return "Shell search tool is missing or not on PATH. Call toolkit `grep` directly: its engine prefers rg but falls back to the builtin scanner, so no install is required. Do not replay the shell search unchanged."
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
	// Path-position globs are checked by searchPathGlobLiteralToken after the
	// effective workdir is resolved: a glob that really expands to existing
	// files is a legitimate search; literal-reaching globs are soft-redirected
	// (not hard-failed) by bashSearchPathGlobRedirectResult.
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

// searchPathGlobCandidate is one path-position token of an rg/grep command that
// carries shell glob metacharacters.
type searchPathGlobCandidate struct {
	Token  string
	Quoted bool // the whole token came from a quoted span, so the shell never expands it
}

// searchPathGlobCandidates returns every path-position glob token in an rg/grep
// command, preserving whether the token was fully quoted. The first positional
// after the search tool is the pattern (a regex may legally contain * ? [])
// unless the pattern comes from -e/--regexp/-f/--file or the tool runs a
// patternless mode (rg --files); only then are all positionals paths. -g/--glob
// values are legitimate and were already consumed as flag values.
func searchPathGlobCandidates(command string) []searchPathGlobCandidate {
	if !looksLikeSearchShellCommand(command) {
		return nil
	}
	tokens := runtimeexecutor.SplitCommandTokensDetailed(command)
	searchIdx := -1
	searchTool := ""
	for i, token := range tokens {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(token.Text)))
		base = strings.TrimSuffix(base, ".exe")
		if isRipgrepOrGrepTool(base) {
			searchIdx = i
			searchTool = base
			break
		}
	}
	if searchIdx < 0 {
		return nil
	}
	var candidates []searchPathGlobCandidate
	positional := 0
	patternFromFlag := false
	patternlessMode := false
	flagsEnded := false
	for i := searchIdx + 1; i < len(tokens); i++ {
		part := strings.TrimSpace(tokens[i].Text)
		if part == "" {
			continue
		}
		// Stop at common shell chain/pipe tokens; only inspect the search command.
		if part == "|" || part == "||" || part == "&&" || part == ";" {
			break
		}
		if !flagsEnded && part == "--" {
			flagsEnded = true
			continue
		}
		if !flagsEnded && strings.HasPrefix(part, "-") {
			if searchFlagSuppliesPattern(part) {
				patternFromFlag = true
			}
			if searchFlagIsPatternlessMode(part) {
				patternlessMode = true
			}
			if searchFlagConsumesNextValue(part, searchTool) && !searchFlagHasInlineValue(part) {
				// Skip the flag value; combined forms like -g*.go already carry it.
				if i+1 < len(tokens) && !strings.HasPrefix(strings.TrimSpace(tokens[i+1].Text), "-") {
					i++
				}
			}
			continue
		}
		positional++
		// The first positional is the pattern unless a flag supplied it or the
		// search mode has no pattern at all.
		if positional == 1 && !patternFromFlag && !patternlessMode {
			continue
		}
		// Remaining positionals are paths: globs here are the residual failure mode.
		if pathTokenHasShellGlob(part) {
			candidates = append(candidates, searchPathGlobCandidate{Token: part, Quoted: tokens[i].FullyQuoted})
		}
	}
	return candidates
}

// pathGlobResolution classifies how the shell will treat a path-position glob.
type pathGlobResolution int

const (
	// pathGlobExpands: the shell expands the token to at least one real path
	// under the command's workdir, so the search tool receives valid paths.
	pathGlobExpands pathGlobResolution = iota
	// pathGlobLiteral: the literal glob (or a shell that never expands
	// native-command arguments) reaches rg/grep and fails with a path error.
	pathGlobLiteral
	// pathGlobUnknown: the token depends on shell state the runtime cannot
	// evaluate statically, so preflight must not block it.
	pathGlobUnknown
)

// shellExpandsUnquotedGlobs reports whether the runtime's active shell performs
// filename expansion on unquoted tokens. POSIX shells do; PowerShell/cmd pass
// wildcards through to native commands untouched (rg then reports os error 123
// on Windows), so globs there still need the -g / toolkit-grep rewrite.
func shellExpandsUnquotedGlobs(shell runtimeexecutor.Shell) bool {
	switch shell.Type {
	case runtimeexecutor.ShellTypeBash, runtimeexecutor.ShellTypeZsh, runtimeexecutor.ShellTypeSh:
		return true
	default:
		return false
	}
}

// classifyPathGlobResolution decides whether a path-position glob would reach
// rg/grep literally - the failure this guard exists for - or expand to real
// files. The glob is evaluated against workdir, the same directory the shell
// runs in.
//
// Unresolvable shell syntax deliberately yields pathGlobUnknown instead of a
// block: brace expansion, $VAR/`command` substitution, ~user, zsh's recursive
// ** (which can match deeper than filepath.Glob's shell-agnostic ** == *), and
// commands that cd before searching. Post-execution diagnostics still explain
// real path IO failures.
func classifyPathGlobResolution(token string, quoted bool, workdir string, shell runtimeexecutor.Shell, shellExpandsGlobs bool) pathGlobResolution {
	if !shellExpandsGlobs {
		return pathGlobLiteral
	}
	if quoted {
		return pathGlobLiteral
	}
	pattern, ok := resolvePathGlobPattern(token)
	if !ok {
		return pathGlobUnknown
	}
	if !filepath.IsAbs(pattern) {
		base := strings.TrimSpace(workdir)
		if base == "" {
			base = "."
		}
		pattern = filepath.Join(base, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// Malformed patterns (e.g. an unclosed `[`) are passed through
		// literally by POSIX shells as well.
		return pathGlobLiteral
	}
	if len(shellVisibleGlobMatches(matches, pattern)) > 0 {
		return pathGlobExpands
	}
	if shell.Type == runtimeexecutor.ShellTypeZsh && strings.Contains(token, "**") {
		// zsh expands ** recursively by default, so a shallow filepath.Glob
		// miss does not prove the token would reach the tool literally.
		return pathGlobUnknown
	}
	return pathGlobLiteral
}

// resolvePathGlobPattern normalizes a leading ~ and rejects token syntax whose
// expansion depends on shell state the runtime cannot evaluate statically.
func resolvePathGlobPattern(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	if strings.ContainsAny(token, "$`") {
		return "", false
	}
	if strings.Contains(token, "{") && strings.Contains(token, "}") {
		return "", false
	}
	switch {
	case strings.HasPrefix(token, "~/"), strings.HasPrefix(token, `~\`):
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", false
		}
		rest := strings.TrimLeft(strings.TrimPrefix(token, "~"), `/\`)
		return filepath.Join(home, rest), true
	case strings.HasPrefix(token, "~"):
		// ~user depends on the user database.
		return "", false
	}
	return token, true
}

// shellVisibleGlobMatches filters filepath.Glob output to what a POSIX shell
// would actually produce: a wildcard never matches a leading dot. Only the
// final component is modeled, which covers the common `dir/*.go` case; a match
// reached through a dot-prefixed intermediate directory may still be counted as
// visible, keeping the guard permissive rather than over-blocking.
func shellVisibleGlobMatches(matches []string, pattern string) []string {
	if len(matches) == 0 {
		return nil
	}
	lastComponent := pattern
	if idx := strings.LastIndexAny(lastComponent, `/\`); idx >= 0 {
		lastComponent = lastComponent[idx+1:]
	}
	if strings.HasPrefix(lastComponent, ".") {
		return matches
	}
	visible := matches[:0]
	for _, match := range matches {
		if strings.HasPrefix(filepath.Base(match), ".") {
			continue
		}
		visible = append(visible, match)
	}
	return visible
}

// searchPathGlobLiteralToken returns the first path-position glob in command
// that would reach rg/grep literally under workdir: the runtime shell never
// expands it, the token is quoted, or no existing path matches. A glob that
// really expands - e.g. `grep -rn x cmd/aicli/commands/*.go` on bash - returns
// ok=false and runs normally.
func searchPathGlobLiteralToken(command, workdir string) (string, bool) {
	candidates := searchPathGlobCandidates(command)
	if len(candidates) == 0 {
		return "", false
	}
	if searchRunsAfterDirectoryChange(runtimeexecutor.SplitCommandTokens(command)) {
		// `cd other/dir && rg foo *.go`: the glob resolves against the cd
		// target, not the effective workdir, so do not guess.
		return "", false
	}
	shell := runtimeexecutor.DefaultUserShell()
	expandsGlobs := shellExpandsUnquotedGlobs(shell)
	for _, candidate := range candidates {
		if classifyPathGlobResolution(candidate.Token, candidate.Quoted, workdir, shell, expandsGlobs) == pathGlobLiteral {
			return candidate.Token, true
		}
	}
	return "", false
}

// searchPathGlobReachesTool reports whether command contains a path-position
// glob that would reach rg/grep literally under workdir.
func searchPathGlobReachesTool(command, workdir string) bool {
	_, reaches := searchPathGlobLiteralToken(command, workdir)
	return reaches
}

// searchToolLookPath and ripgrepShellDir are stubbable so tests do not depend
// on the host PATH or on the resolver's environment.
var (
	searchToolLookPath = exec.LookPath
	ripgrepShellDir    = runtimeripgrep.ShellPrependDir
)

// shellRipgrepAvailable reports whether a plain `rg` invocation can run in the
// command shell: either PATH/PATHEXT already resolves it, or command execution
// will prepend the resolver-selected canonical rg directory to PATH.
func shellRipgrepAvailable() bool {
	if path, err := searchToolLookPath("rg"); err == nil && strings.TrimSpace(path) != "" {
		return true
	}
	_, ok := ripgrepShellDir()
	return ok
}

// bashSearchPathGlobRedirectResult soft-redirects a shell search whose
// path-position glob would reach rg/grep literally. The command is not
// executed; the model gets the same success-shaped contract as the simple
// shell-search redirect plus a concrete -g rewrite derived from the offending
// token (dir/*_test.go -> `rg -g "*_test.go" <pattern> dir`). When no rg can
// run in the shell, the rewrite is omitted and toolkit grep (whose builtin
// scanner needs no rg) becomes the only suggested path.
func bashSearchPathGlobRedirectResult(globToken string) *toolkit.ToolResult {
	metadata := map[string]interface{}{
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"shell_search_redirected":     true,
		"shell_path_glob_redirected":  true,
		"redirect_tool":               "grep",
		"executed":                    false,
		"path_glob":                   globToken,
	}
	rgAvailable := shellRipgrepAvailable()
	metadata["shell_rg_available"] = rgAvailable
	if !rgAvailable {
		metadata[toolresult.MetadataNextActionKey] = "Call toolkit `grep` with path and glob. Its engine prefers rg but falls back to the builtin scanner, so a missing rg binary does not block the search; do not replay the shell path glob unchanged."
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"未执行：搜索命令的 path 参数 `%s` 含 shell 通配符，会被原样传给 rg/grep 并触发路径错误（当前 shell 不展开该通配符，或它在 workdir 下没有任何匹配）。这不是无匹配结果，也不是 hard failure。当前 shell PATH 上没有可用的 `rg`，请把通配符交给 toolkit `grep` 的 path + glob 参数：它的引擎优先使用 rg，不可用时自动回退内置扫描，无需安装 ripgrep。",
				globToken,
			),
			Metadata: metadata,
		}
	}
	rewrite := searchPathGlobRewriteSuggestion(globToken)
	rewriteHint := "`rg -g \"*.go\" <pattern> <真实目录>`"
	nextAction := "Prefer toolkit `grep` with path and glob. If shell rg is required, use `-g \"*.go\"` / `--glob` and keep path as a real directory; do not pass a path glob that matches no existing path."
	if rewrite != "" {
		rewriteHint = rewrite
		nextAction = fmt.Sprintf("Re-run with the glob moved into the search tool filter, e.g. %s, or call toolkit `grep` with path + glob. Do not replay the same path glob unchanged.", rewrite)
	}
	content := fmt.Sprintf(
		"未执行：搜索命令的 path 参数 `%s` 含 shell 通配符，会被原样传给 rg/grep 并触发路径错误（当前 shell 不展开该通配符，或它在 workdir 下没有任何匹配；Windows 常见 os error 123）。这不是无匹配结果，也不是 hard failure。请改用 %s（`-g` 会把通配符交给 rg/grep 自身，且通常递归子目录；若只需该目录一层，请改为显式文件列表），或直接调用 toolkit `grep`（path + glob）。",
		globToken, rewriteHint,
	)
	metadata[toolresult.MetadataNextActionKey] = nextAction
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata:   metadata,
	}
}

// searchPathGlobRewriteSuggestion derives the concrete -g rewrite for a
// path-position glob. Directory parts that are themselves globs (or a bare
// filename glob) fall back to placeholders so the suggestion never contains
// another literal path glob that would fail the same way.
func searchPathGlobRewriteSuggestion(globToken string) string {
	parts := strings.Split(filepath.ToSlash(strings.TrimSpace(globToken)), "/")
	pattern := ""
	dirParts := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" || part == "**" {
			continue
		}
		if pattern == "" {
			pattern = part
			continue
		}
		dirParts = append([]string{part}, dirParts...)
	}
	if pattern == "" {
		return ""
	}
	dir := strings.Join(dirParts, "/")
	if dir == "." {
		dir = ""
	}
	if dir == "" || hasGlobMeta(dir) {
		return fmt.Sprintf("`rg -g %q <pattern> <真实目录>`", pattern)
	}
	return fmt.Sprintf("`rg -g %q <pattern> %s`", pattern, dir)
}

// searchRunsAfterDirectoryChange reports whether a cd/chdir/Set-Location token
// appears before the rg/grep invocation; workdir-based glob resolution is
// unreliable for those commands.
func searchRunsAfterDirectoryChange(tokens []string) bool {
	for _, token := range tokens {
		lower := strings.ToLower(strings.TrimSpace(token))
		lower = strings.TrimSuffix(lower, ".exe")
		if isRipgrepOrGrepTool(lower) {
			return false
		}
		switch lower {
		case "cd", "chdir", "set-location":
			return true
		}
	}
	return false
}

// searchFlagConsumesNextValue reports whether flag takes the next token as its
// value. Short flags are matched case-sensitively (grep -f FILE vs -F
// fixed-strings; rg -r REPLACE vs grep -r recursive) and tool-aware where the
// meaning differs between rg and grep.
func searchFlagConsumesNextValue(flag, tool string) bool {
	raw := strings.TrimSpace(flag)
	lower := strings.ToLower(raw)
	if searchFlagHasInlineValue(raw) {
		return true
	}
	switch lower {
	case "--regexp", "--file", "--glob", "--iglob", "--type", "--type-not",
		"--max-count", "--after-context", "--before-context", "--context",
		"--max-depth", "--max-filesize", "--sort", "--sortr", "--replace",
		"--type-add", "--type-clear", "--ignore-file", "--engine",
		"--field-context-separator", "--path-separator", "--context-separator":
		return true
	}
	switch raw {
	case "-e", "-f", "-g", "-t", "-T", "-m", "-A", "-B", "-C":
		return true
	case "-r":
		// rg -r/--replace takes a value; grep -r is recursive and takes none.
		return tool == "rg" || tool == "ripgrep"
	}
	return false
}

// searchFlagSuppliesPattern reports whether the flag itself supplies the search
// pattern (-e/--regexp, -f/--file, including inline forms), which means the
// first positional token is a path rather than the pattern. Matching stays
// case-sensitive so grep -F (fixed strings) is not mistaken for -f FILE.
func searchFlagSuppliesPattern(flag string) bool {
	raw := strings.TrimSpace(flag)
	lower := strings.ToLower(raw)
	if eq := strings.IndexByte(lower, '='); eq >= 0 {
		switch lower[:eq] {
		case "--regexp", "--file":
			return true
		}
	}
	switch lower {
	case "--regexp", "--file":
		return true
	}
	switch {
	case raw == "-e", raw == "-f":
		return true
	case strings.HasPrefix(raw, "-e") && !strings.HasPrefix(raw, "-e-"):
		return len(raw) > 2
	case strings.HasPrefix(raw, "-f"):
		return len(raw) > 2
	}
	return false
}

// searchFlagIsPatternlessMode reports search modes that take no pattern, so
// every positional token is a path (rg --files).
func searchFlagIsPatternlessMode(flag string) bool {
	lower := strings.ToLower(strings.TrimSpace(flag))
	if eq := strings.IndexByte(lower, '='); eq >= 0 {
		lower = lower[:eq]
	}
	return lower == "--files"
}

func searchFlagHasInlineValue(flag string) bool {
	raw := strings.TrimSpace(flag)
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "--") {
		return strings.Contains(lower, "=")
	}
	// Short combined flags: -g*.go, -ePATTERN, -fFILE; -m10/-A2 stay flag+value
	// pairs and are handled by searchFlagConsumesNextValue.
	switch {
	case strings.HasPrefix(raw, "-g") && raw != "-g":
		return true
	case strings.HasPrefix(raw, "-e") && raw != "-e" && !strings.HasPrefix(raw, "-e-"):
		// -ePATTERN is uncommon but possible; treat non-exact -e* carefully.
		return len(raw) > 2
	case strings.HasPrefix(raw, "-f") && raw != "-f":
		// -fFILE mirrors grep/getopt attached-value form; -F stays fixed-strings.
		return len(raw) > 2
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
