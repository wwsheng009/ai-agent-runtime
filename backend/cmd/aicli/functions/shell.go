package functions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// ShellFunction 执行 shell 命令的 Function
type ShellFunction struct {
	executer CommandExecuter
}

const modelHistoryArtifactThresholdBytes = 12 * 1024

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

	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

	// 获取命令输出
	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifact(cmd, captureLimitBytesFromExecConfig(cfg), "function", command, "")
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "function", command)
	outputStr := capture.Output
	metadata := buildShellExecutionMetadata(command, outputStr, capture, artifactPath, artifactErr, shell)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ShellExecutionResult{Output: outputStr, Metadata: metadata}, fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 针对常见错误给出友好提示
		friendlyHint := friendlyHintForCommand(command, outputStr, err)
		if friendlyHint != "" {
			return ShellExecutionResult{Output: outputStr, Metadata: metadata}, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, getShellEnvironmentInfo())
		}
		return ShellExecutionResult{Output: outputStr, Metadata: metadata}, fmt.Errorf("命令执行失败: %w\n\n当前环境信息:\n%s", err, getShellEnvironmentInfo())
	}

	return ShellExecutionResult{Output: outputStr, Metadata: metadata}, nil
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
func friendlyHintForCommand(command string, output string, err error) string {
	cmdLower := strings.ToLower(command)
	cmdParts := strings.Fields(command)

	mainCmd := ""
	if len(cmdParts) > 0 {
		mainCmd = strings.ToLower(cmdParts[0])
	}

	exitCode := -1
	if exitError, ok := err.(*exec.ExitError); ok {
		exitCode = exitError.ExitCode()
	}

	switch {
	case cmdLower == "pwd" && runtimeexecutor.IsWindows():
		if runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
			return "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
		}
		return ""
	case runtimeexecutor.IsWindows() &&
		(strings.Contains(cmdLower, "| head") ||
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
	case strings.Contains(strings.ToLower(output), "permission") ||
		strings.Contains(strings.ToLower(output), "access") && strings.Contains(strings.ToLower(output), "denied"):
		return "提示: 权限不足，请检查是否有执行该命令的权限"
	case strings.Contains(output, "no such file or directory") ||
		strings.Contains(output, "cannot find the path"):
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

func buildShellExecutionMetadata(command string, output string, capture runtimeexecutor.CombinedOutputCapture, artifactPath string, artifactErr error, shell runtimeexecutor.Shell) map[string]interface{} {
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
		disable, ok := rawDisable.(bool)
		if !ok {
			return nil, fmt.Errorf("disable_output_cap 参数必须是布尔值")
		}
		disableOutputCap = disable
	}

	hasOutputBytesCap := false
	outputBytesCap := 0
	if rawCap, ok := args["output_bytes_cap"]; ok {
		value, err := extractPositiveInt(rawCap)
		if err != nil {
			return nil, fmt.Errorf("output_bytes_cap 参数无效: %w", err)
		}
		outputBytesCap = value
		hasOutputBytesCap = true
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
	return "在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。切换目录优先使用 workdir 参数；不要用裸 cd 验证当前目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。路径建议使用正斜杠格式（如 E:/projects/foo）以确保跨平台兼容。"
}

// Parameters 返回 Function 参数的 JSON Schema 描述
func (f *ShellFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）。切换目录优先使用 workdir 参数；查看当前目录用 pwd/Get-Location（PowerShell/pwsh）或 cd/echo %cd%（cmd），不要用裸 cd 来验证目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。路径请使用正斜杠（如 E:/projects/foo）。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。路径请使用正斜杠（如 E:/projects/foo）以兼容所有平台。",
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

	// 执行命令，30秒超时
	if rich, ok := f.executer.(DetailedCommandExecuter); ok {
		result, err := rich.ExecuteDetailed(ctx, command, 30*time.Second, opts...)
		return result.Output, result.Metadata, err
	}
	output, err := f.executer.Execute(ctx, command, 30*time.Second, opts...)
	return output, nil, err
}

// SetExecuter 设置命令执行器（用于测试）
func (f *ShellFunction) SetExecuter(executer CommandExecuter) {
	f.executer = executer
}
