package tools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/shellexec"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// executeDirectForTests preserves the pre-job executor coverage. It is compiled
// only into the package test binary and is never reachable in production.
func (b *BashTool) executeDirectForTests(ctx context.Context, command, workdir string, timeout time.Duration, timeoutSource string, captureSettings outputCaptureSettings, mutatedPaths []string) (*toolkit.ToolResult, error) {
	_ = timeoutSource
	started := time.Now()
	execResult, err := b.executeCommand(ctx, command, workdir, timeout, captureSettings)
	duration := time.Since(started)
	if err != nil {
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
			if next := bashCommandFailureNextAction(command, execResult.Output, err, workdir); next != "" {
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
		annotateTerminationMetadata(failureMetadata, err)
		if next := bashCommandFailureNextAction(command, execResult.Output, err, workdir); next != "" {
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
			recoveryHint = "命令本体已结束，但仍有后代进程占用输出管道；runtime 已按 WaitDelay 收敛，输出可能不完整。若该进程是有意保留的守护进程，请改用 job_start，或在命令内显式等待/终止该子进程。"
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
			"",
			failureMetadata,
		)
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
		Content:    formatShellCommandContent(0, execResult.ShellType, workdir, duration, false, execResult.Output),
		Metadata:   metadata,
	}, nil
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
	commandForShell := runtimeexecutor.WrapPowerShellExitCode(shell, command)
	shellCmd := shell.DeriveExecArgs(command, false)

	if err := b.sandbox.CheckCommandDenied(shellCmd[0]); err != nil {
		return CommandExecutionResult{}, wrapSandboxPermissionError("sandbox denied shell launcher", err, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
			"launcher":  shellCmd[0],
		})
	}

	outputMirror := resolveToolTerminalOutputMirror(ctx)
	artifactRoot := toolctx.ShellOutputArtifactDir(ctx)
	execResult, runErr := shellexec.Run(cmdCtx, shellexec.Options{
		Command:       commandForShell,
		Workdir:       resolvedWorkdir,
		Env:           runtimeexecutor.BuildFilteredEnv(b.sandbox, os.Environ()),
		CaptureLimit:  captureSettings.captureLimitBytes(),
		ArtifactScope: "toolkit",
		ArtifactRoot:  artifactRoot,
		Mirror:        outputMirror,
		Shell:         shell,
		Invocation: func(command string) (string, []string) {
			argv := shell.DeriveExecArgs(command, false)
			return argv[0], argv[1:]
		},
		Prepare: func(prepareCtx context.Context, launch shellexec.Launch) (shellexec.Launch, error) {
			if err := b.sandbox.CheckCommandDenied(launch.Command); err != nil {
				return shellexec.Launch{}, wrapSandboxPermissionError("sandbox denied shell launcher", err, map[string]interface{}{
					"policy":    "sandbox",
					"operation": string(runtimeexecutor.OpExecute),
					"command":   mainCmd,
					"launcher":  launch.Command,
				})
			}
			wrapped, wrapErr := b.sandbox.PrepareOSCommand(prepareCtx, launch.Command, launch.Args, launch.Workdir, launch.Env)
			if wrapErr != nil {
				return shellexec.Launch{}, wrapSandboxPermissionError("sandbox denied os isolation", wrapErr, map[string]interface{}{
					"policy":    "sandbox",
					"operation": string(runtimeexecutor.OpExecute),
					"command":   mainCmd,
					"launcher":  launch.Command,
					"os_mode":   b.sandbox.Config().OSSandbox,
				})
			}
			prepared := shellexec.Launch{
				Command: wrapped.Command,
				Args:    append([]string(nil), wrapped.Args...),
				Env:     wrapped.Env,
				Workdir: launch.Workdir,
			}
			if strings.TrimSpace(wrapped.WorkDir) != "" {
				prepared.Workdir = wrapped.WorkDir
			}
			if wrapped.Env == nil {
				prepared.Env = launch.Env
			}
			return prepared, nil
		},
	})
	if !execResult.Started && runErr != nil {
		return CommandExecutionResult{}, runErr
	}
	capture := execResult.Capture
	artifactPath := execResult.ArtifactPath
	artifactErr := execResult.ArtifactError
	err = runErr
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command, artifactRoot)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			result = applyCommandExecutionShell(result, shell)
			result = applyProcessGuardResult(result, execResult, err, "timeout")
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
			result = applyProcessGuardResult(result, execResult, err, "cancel")
			applyCommandTimeoutBudget(&result, budget)
			return result, runtimeexecution.ContextCancellationError(ctx)
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		result = applyCommandExecutionShell(result, shell)
		result = applyProcessGuardResult(result, execResult, err, "error")
		applyCommandTimeoutBudget(&result, budget)
		return result, err
	}
	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	result = applyCommandExecutionShell(result, shell)
	result = applyProcessGuardResult(result, execResult, err, "")
	applyCommandTimeoutBudget(&result, budget)
	return result, nil
}
