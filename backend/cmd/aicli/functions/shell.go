package functions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// ShellFunction 执行 shell 命令的 Function
type ShellFunction struct {
	executer CommandExecuter
}

const (
	modelHistoryArtifactThresholdBytes = 12 * 1024
	defaultShellFunctionTimeout        = 30 * time.Second
	shellFunctionTimeoutEnv            = "AICLI_SHELL_COMMAND_TIMEOUT"
	shellFunctionTimeoutMSEnv          = "AICLI_SHELL_COMMAND_TIMEOUT_MS"
)

type ShellExecutionResult struct {
	Output   string
	Metadata map[string]interface{}
}

// CommandExecuter 命令执行器接口（用于测试和自定义）
type CommandExecuter interface {
	Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error)
}

// DetailedCommandExecuter optionally returns richer metadata in addition to text output.
type DetailedCommandExecuter interface {
	CommandExecuter
	ExecuteDetailed(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (ShellExecutionResult, error)
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

// WithOutputBytesCap overrides the retained shell output cap.
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

// Execute 执行命令
func (e *DefaultCommandExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error) {
	result, err := e.ExecuteDetailed(ctx, command, timeout, opts...)
	return result.Output, err
}

// ExecuteDetailed executes the command and returns capture metadata.
func (e *DefaultCommandExecuter) ExecuteDetailed(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (ShellExecutionResult, error) {
	cfg := &execConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	budget := runtimeexecution.ResolveTimeout(ctx, timeout)
	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(ctx, budget.Effective)
	defer cancel()

	// 使用智能 shell 检测
	shell := runtimeexecutor.DefaultUserShell()
	shellArgs := shell.DeriveExecArgs(command, false)

	// 执行命令
	cmd := exec.CommandContext(ctx, shellArgs[0], shellArgs[1:]...)

	// 设置工作目录
	if cfg.workdir != "" {
		cmd.Dir = cfg.workdir
	}

	// 过滤敏感环境变量
	cmd.Env = runtimeexecutor.FilterSensitiveEnv(os.Environ())

	// PowerShell 需要 UTF-8 输出编码
	if shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh {
		prefixPowershellUTF8ForCmd(cmd)
	}

	outputMirror := runtimeexecutor.OutputMirrorFromContext(ctx)
	if outputMirror != nil {
		runtimeexecutor.PrepareCommandForLowLatencyOutput(cmd)
	}

	started := time.Now()
	// 获取命令输出
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifactAndMirror(cmd, captureLimitBytesFromExecConfig(cfg), "function", command, "", outputMirror)
	duration := time.Since(started)
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "function", command)
	outputStr := capture.Output
	metadata := buildShellExecutionMetadata(command, outputStr, capture, artifactPath, artifactErr, shell, cfg.workdir, budget)
	if duration > 0 {
		metadata["duration_ms"] = duration.Milliseconds()
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			metadata["timed_out"] = true
			return ShellExecutionResult{Output: outputStr, Metadata: metadata}, runtimeexecution.TimeoutError(budget)
		}
		if ctx.Err() == context.Canceled {
			return ShellExecutionResult{Output: outputStr, Metadata: metadata}, runtimeexecution.ContextCancellationError(ctx)
		}

		// Completed process with a non-zero exit is content success (Codex-like).
		// Only launch/control-plane failures remain hard tool errors.
		if !isHardShellFunctionError(err) {
			exitCode := exitCodeFromShellError(err)
			if exitCode < 0 {
				exitCode = 1
			}
			metadata["exit_code"] = exitCode
			metadata["non_zero_exit"] = exitCode != 0
			content := formatShellFunctionContent(exitCode, string(shell.Type), cfg.workdir, duration, false, outputStr)
			if hint := friendlyHintForCommand(command, outputStr, err, cfg.workdir); hint != "" {
				content = strings.TrimRight(content, "\n") + "\n" + hint
			}
			return ShellExecutionResult{Output: content, Metadata: metadata}, nil
		}

		// 针对常见错误给出友好提示
		friendlyHint := friendlyHintForCommand(command, outputStr, err, cfg.workdir)
		if friendlyHint != "" {
			return ShellExecutionResult{Output: outputStr, Metadata: metadata}, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, getShellEnvironmentInfo())
		}
		return ShellExecutionResult{Output: outputStr, Metadata: metadata}, fmt.Errorf("命令执行失败: %w\n\n当前环境信息:\n%s", err, getShellEnvironmentInfo())
	}

	metadata["exit_code"] = 0
	metadata["non_zero_exit"] = false
	return ShellExecutionResult{Output: outputStr, Metadata: metadata}, nil
}

// isHardShellFunctionError reports true only for control-plane / launch failures.
// A finished process with non-zero exit code is NOT a hard failure.
func isHardShellFunctionError(err error) bool {
	if err == nil {
		return false
	}
	if runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout) ||
		runtimeerrors.Is(err, runtimeerrors.ErrTurnDeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	if exitCodeFromShellError(err) >= 0 {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	return true
}

// exitCodeFromShellError extracts an exit status from *exec.ExitError or
// common "exit status N" / hex status messages after wrapping.
func exitCodeFromShellError(err error) int {
	if err == nil {
		return -1
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	msg := strings.TrimSpace(err.Error())
	const prefix = "exit status "
	if idx := strings.LastIndex(strings.ToLower(msg), prefix); idx >= 0 {
		token := strings.TrimSpace(msg[idx+len(prefix):])
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

// formatShellFunctionContent builds a Codex-like model-facing shell result body.
func formatShellFunctionContent(exitCode int, shellType, workdir string, duration time.Duration, timedOut bool, output string) string {
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

// prefixPowershellUTF8ForCmd prepends a UTF-8 encoding command for PowerShell.
func prefixPowershellUTF8ForCmd(cmd *exec.Cmd) {
	if len(cmd.Args) < 3 {
		return
	}
	lastIdx := len(cmd.Args) - 1
	cmd.Args[lastIdx] = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + cmd.Args[lastIdx]
}

// friendlyHintForCommand returns a user-friendly hint when a command fails.
func friendlyHintForCommand(command string, output string, err error, workdir string) string {
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
		if runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
			return "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
		}
		return ""
	case runtimeexecutor.IsWindows() &&
		(runtimeexecutor.HasPipedHeadToken(cmdParts) ||
			mainCmd == "head" ||
			strings.Contains(strings.ToLower(output), "the term 'head' is not recognized")):
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

// getShellEnvironmentInfo returns environment info including detected shell.
func getShellEnvironmentInfo() string {
	shell := runtimeexecutor.DefaultUserShell()
	return fmt.Sprintf("系统类型: %s\nShell: %s (%s)", runtimeexecutor.GoOS(), shell.Type, shell.Path)
}

// GetEnvironmentInfo 获取基础环境信息（用于命令执行失败时报告）
// Deprecated: Use getShellEnvironmentInfo instead for better shell detection.
func GetEnvironmentInfo() string {
	return getShellEnvironmentInfo()
}

func buildShellExecutionMetadata(command string, output string, capture runtimeexecutor.CombinedOutputCapture, artifactPath string, artifactErr error, shell runtimeexecutor.Shell, workdir string, budget runtimeexecution.TimeoutBudget) map[string]interface{} {
	retainedBytes := capture.RetainedBytes
	if retainedBytes == 0 && output != "" {
		retainedBytes = len(output)
	}
	totalBytes := capture.TotalBytes
	if totalBytes == 0 && output != "" {
		totalBytes = len(output)
	}
	totalLines := capture.TotalLines
	if totalLines == 0 && strings.TrimSpace(output) != "" {
		totalLines = strings.Count(strings.ReplaceAll(output, "\r\n", "\n"), "\n") + 1
	}
	metadata := map[string]interface{}{
		"command":                       command,
		"command_length_bytes":          len(command),
		"timeout_ms":                    budget.Effective.Milliseconds(),
		"timeout_requested_ms":          budget.Requested.Milliseconds(),
		"timeout_effective_ms":          budget.Effective.Milliseconds(),
		"timeout_source":                string(budget.Source),
		"output_size":                   len(output),
		"captured_output_bytes":         retainedBytes,
		"retained_output_bytes":         retainedBytes,
		"total_output_bytes":            totalBytes,
		"total_output_lines":            totalLines,
		"output_capture_complete":       !capture.Truncated,
		"output_truncated":              capture.Truncated,
		"capture_limit_reached":         capture.Truncated,
		"output_capture_limit_disabled": capture.CaptureLimitDisabled,
		"executed_at":                   time.Now().Unix(),
	}
	if trimmed := strings.TrimSpace(workdir); trimmed != "" {
		metadata["workdir"] = trimmed
	}
	if !capture.CaptureLimitDisabled && capture.CaptureLimitBytes > 0 {
		metadata["output_capture_limit_bytes"] = capture.CaptureLimitBytes
	}
	if capture.OmittedBytes > 0 {
		metadata["omitted_output_bytes"] = capture.OmittedBytes
	}
	if strings.TrimSpace(artifactPath) != "" {
		metadata["raw_output_artifact_path"] = artifactPath
	}
	if artifactErr != nil {
		metadata["raw_output_artifact_error"] = artifactErr.Error()
	}
	for key, value := range shell.Metadata() {
		metadata[key] = value
	}
	return metadata
}

func ensureLargeHistoryOutputArtifact(capture runtimeexecutor.CombinedOutputCapture, artifactPath string, artifactErr error, scope string, command string) (string, error) {
	if strings.TrimSpace(artifactPath) != "" || artifactErr != nil || capture.Truncated {
		return artifactPath, artifactErr
	}
	if capture.TotalBytes <= modelHistoryArtifactThresholdBytes || strings.TrimSpace(capture.Output) == "" {
		return artifactPath, artifactErr
	}
	path, err := runtimeexecutor.PersistShellOutputArtifact(scope, command, "", capture.Output)
	if err != nil {
		return "", err
	}
	return path, nil
}

func captureLimitBytesFromExecConfig(cfg *execConfig) int {
	if cfg == nil {
		return runtimeexecutor.DefaultRetainedOutputBytes
	}
	if cfg.disableOutputCap {
		return runtimeexecutor.DisableRetainedOutputLimit
	}
	if cfg.hasOutputBytesCap {
		return cfg.outputBytesCap
	}
	return runtimeexecutor.DefaultRetainedOutputBytes
}

func buildOutputCaptureExecOptions(args map[string]interface{}) ([]ExecOption, error) {
	if args == nil {
		return nil, nil
	}

	disableOutputCap := false
	if rawDisable, ok := args["disable_output_cap"]; ok {
		if rawDisable != nil {
			disable, ok := rawDisable.(bool)
			if !ok {
				return nil, fmt.Errorf("disable_output_cap 参数必须是布尔值")
			}
			disableOutputCap = disable
		}
	}

	hasOutputBytesCap := false
	outputBytesCap := 0
	if rawCap, ok := args["output_bytes_cap"]; ok {
		if rawCap != nil {
			value, err := extractPositiveInt(rawCap)
			if err != nil {
				return nil, fmt.Errorf("output_bytes_cap 参数无效: %w", err)
			}
			outputBytesCap = value
			hasOutputBytesCap = true
		}
	}

	if disableOutputCap && hasOutputBytesCap {
		return nil, fmt.Errorf("output_bytes_cap 不能与 disable_output_cap 同时设置")
	}

	opts := make([]ExecOption, 0, 1)
	if hasOutputBytesCap {
		opts = append(opts, WithOutputBytesCap(outputBytesCap))
	}
	if disableOutputCap {
		opts = append(opts, WithDisableOutputCap())
	}
	return opts, nil
}

func parseShellFunctionTimeout(args map[string]interface{}, defaultTimeout time.Duration) (time.Duration, error) {
	if defaultTimeout <= 0 {
		defaultTimeout = resolveDefaultShellFunctionTimeout()
	}
	if args == nil {
		return defaultTimeout, nil
	}

	if raw, ok := args["timeout_ms"]; ok && raw != nil {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_ms 参数无效: %w", err)
		}
		return time.Duration(value) * time.Millisecond, nil
	}
	if raw, ok := args["timeout_sec"]; ok && raw != nil {
		value, err := extractPositiveInt(raw)
		if err != nil {
			return 0, fmt.Errorf("timeout_sec 参数无效: %w", err)
		}
		return time.Duration(value) * time.Second, nil
	}
	if raw, ok := args["timeout"]; ok && raw != nil {
		timeoutText, ok := raw.(string)
		if !ok {
			return defaultTimeout, nil
		}
		timeoutText = strings.TrimSpace(timeoutText)
		if timeoutText == "" {
			return defaultTimeout, nil
		}
		parsed, err := time.ParseDuration(timeoutText)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("timeout 参数无效: %q", timeoutText)
		}
		return parsed, nil
	}
	return defaultTimeout, nil
}

func hasExplicitShellFunctionTimeout(args map[string]interface{}) bool {
	for _, key := range []string{"timeout_ms", "timeout_sec", "timeout"} {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if text, isText := value.(string); !isText || strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func shellFunctionTimeoutError(timeout time.Duration) error {
	return fmt.Errorf("命令执行超时（超过 %v）。如需继续运行长命令，请重试并显式设置 timeout（例如 2m、5m）或 timeout_ms/timeout_sec", timeout)
}

func resolveDefaultShellFunctionTimeout() time.Duration {
	return resolveShellFunctionTimeoutFromEnv(shellFunctionTimeoutMSEnv, shellFunctionTimeoutEnv, defaultShellFunctionTimeout)
}

func resolveShellFunctionTimeoutFromEnv(timeoutMSEnv string, timeoutEnv string, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = defaultShellFunctionTimeout
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

// NewShellFunction 创建新的 Shell Function
func NewShellFunction() *ShellFunction {
	return &ShellFunction{
		executer: &DefaultCommandExecuter{},
	}
}

// Name 返回 Function 名称
func (f *ShellFunction) Name() string {
	return "execute_shell_command"
}

// Description 返回 Function 描述
func (f *ShellFunction) Description() string {
	return "在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。进程非零退出码会作为内容结果返回（含 Exit code/Output），不是工具崩溃；仅未启动/超时/取消/权限拒绝等才是硬失败。切换目录优先使用 workdir 参数；不要用裸 cd 验证当前目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。路径建议使用正斜杠格式（如 E:/projects/foo）以确保跨平台兼容；路径不存在时会尽量给出候选路径建议。"
}

// Parameters 返回 Function 参数的 JSON Schema 描述
func (f *ShellFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）。切换目录优先使用 workdir 参数；查看当前目录用 pwd/Get-Location（PowerShell/pwsh）或 cd/echo %cd%（cmd），不要用裸 cd 来验证目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。路径请使用正斜杠（如 E:/projects/foo）；路径不存在时会尽量给出候选路径建议。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。路径请使用正斜杠（如 E:/projects/foo）以兼容所有平台。",
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令超时，例如 30s、2m、5m。默认 30s，可用 AICLI_SHELL_COMMAND_TIMEOUT 或 AICLI_SHELL_COMMAND_TIMEOUT_MS 调整全局默认；运行测试、构建、类型检查等可能超过默认值的命令时，应由模型显式设置更长超时。",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "可选：命令超时毫秒数，必须为正整数。优先级高于 timeout 和 timeout_sec。",
			},
			"timeout_sec": map[string]interface{}{
				"type":        "integer",
				"description": "可选：命令超时秒数，必须为正整数。优先级低于 timeout_ms，高于 timeout。",
			},
			"output_bytes_cap": map[string]interface{}{
				"type":        "integer",
				"description": "可选：stdout/stderr 合并输出的保留上限（字节）。用于覆盖默认 256KB capture limit；必须为正整数，不能与 disable_output_cap 同时设置。",
			},
			"disable_output_cap": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时关闭 shell 输出 capture limit，尽量保留完整原始输出；不能与 output_bytes_cap 同时设置。",
			},
		},
		"required": []string{"command"},
	}
}

// Execute 执行 Function
func (f *ShellFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	output, _, err := f.ExecuteWithMeta(ctx, args)
	return output, err
}

// ExecuteWithMeta executes the shell command and returns capture metadata when supported.
func (f *ShellFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", nil, fmt.Errorf("command 参数缺失或不是字符串，args: %+v, command值: %v (类型: %T)", args, args["command"], args["command"])
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return "", nil, fmt.Errorf("command 参数为空")
	}

	// 解析可选的 workdir
	var opts []ExecOption
	if workdir, ok := args["workdir"].(string); ok && strings.TrimSpace(workdir) != "" {
		opts = append(opts, WithWorkdir(strings.TrimSpace(workdir)))
	}
	captureOpts, err := buildOutputCaptureExecOptions(args)
	if err != nil {
		return "", nil, err
	}
	opts = append(opts, captureOpts...)
	timeout, err := parseShellFunctionTimeout(args, resolveDefaultShellFunctionTimeout())
	if err != nil {
		return "", nil, err
	}
	timeoutSource := runtimeexecution.TimeoutSourceToolDefault
	if hasExplicitShellFunctionTimeout(args) {
		timeoutSource = runtimeexecution.TimeoutSourceToolArgument
	}
	ctx = runtimeexecution.WithTimeoutRequestSource(ctx, timeoutSource)

	if rich, ok := f.executer.(DetailedCommandExecuter); ok {
		result, err := rich.ExecuteDetailed(ctx, command, timeout, opts...)
		return result.Output, result.Metadata, err
	}
	output, err := f.executer.Execute(ctx, command, timeout, opts...)
	return output, nil, err
}

// SetExecuter 设置命令执行器（用于测试）
func (f *ShellFunction) SetExecuter(executer CommandExecuter) {
	f.executer = executer
}
