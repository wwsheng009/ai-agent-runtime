package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
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

const modelHistoryArtifactThresholdBytes = 12 * 1024

// NewBashTool 创建 Bash 工具
func NewBashTool() *BashTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command. On Windows this tool uses the detected user shell, usually PowerShell/pwsh rather than cmd.exe. Prefer the workdir parameter for directory changes; use pwd/Get-Location to print the current directory and do not use bare cd for that purpose. If the task contains multiple steps or multiple independent goals, split it into multiple bash calls and keep each call focused on one clear command goal. Windows PowerShell/pwsh does not provide `head` by default; when you need to limit output, prefer `... | Select-Object -First 200`.",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。",
			},
			"output_bytes_cap": map[string]interface{}{
				"type":        "integer",
				"description": "可选：stdout/stderr 合并输出的保留上限（字节）。用于覆盖默认 256KB capture limit；必须为正整数，不能与 disable_output_cap 同时设置。",
			},
			"disable_output_cap": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时关闭 shell 输出 capture limit，尽量保留完整原始输出；不能与 output_bytes_cap 同时设置。",
			},
			"mutated_paths": map[string]interface{}{
				"type":        "array",
				"description": "可选：命令将修改的文件路径列表，用于变更追踪与回滚。",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"required": []string{"command"},
	}

	return &BashTool{
		BaseTool: toolkit.NewBaseTool(
			"bash",
			"执行 Shell 命令并返回输出。Windows 下优先使用检测到的 PowerShell/pwsh；切换目录优先使用 workdir 参数；如果命令包含多个步骤或多个独立目标，请拆分为多次 bash 调用，每次只聚焦一个明确的命令目标。PowerShell/pwsh 默认没有 `head`，限制输出请使用 `Select-Object -First N`。",
			"1.1.0",
			parameters,
			true, // 支持直接调用
		),
		executer:  &DefaultCommandExecuter{},
		timeout:   30 * time.Second,
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
}

type outputCaptureSettings struct {
	outputBytesCap    int
	hasOutputBytesCap bool
	disableOutputCap  bool
}

// Execute 实现 Tool 接口
func (b *BashTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
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

	// 检查黑名单
	if b.isBlacklisted(command) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("命令被禁止执行（安全限制）"),
		}, nil
	}

	// 使用 executer 执行命令
	execResult, err := b.executeCommand(ctx, command, workdir, captureSettings)
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

func (b *BashTool) executeCommand(ctx context.Context, command string, workdir string, captureSettings outputCaptureSettings) (CommandExecutionResult, error) {
	// 解析工作目录
	resolvedWorkdir, err := resolveWorkdir(workdir)
	if err != nil {
		return CommandExecutionResult{}, err
	}

	if b.sandbox == nil {
		opts := []ExecOption{WithWorkdir(resolvedWorkdir)}
		if captureSettings.hasOutputBytesCap {
			opts = append(opts, WithOutputBytesCap(captureSettings.outputBytesCap))
		}
		if captureSettings.disableOutputCap {
			opts = append(opts, WithDisableOutputCap())
		}
		return b.executer.Execute(ctx, command, b.timeout, opts...)
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

	timeout := b.timeout
	if configured := b.sandbox.Config().MaxExecutionTime; configured > 0 {
		timeout = configured
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
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

	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifact(cmd, captureSettings.captureLimitBytes(), "toolkit", command, "")
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			applyCommandExecutionShell(result, shell)
			return result, fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		applyCommandExecutionShell(result, shell)
		return result, err
	}
	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	applyCommandExecutionShell(result, shell)
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

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
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

	capture, artifactPath, err, artifactErr := runtimeexecutor.CaptureCombinedOutputWithArtifact(cmd, captureLimitBytesFromExecConfig(cfg), "toolkit", command, "")
	artifactPath, artifactErr = ensureLargeHistoryOutputArtifact(capture, artifactPath, artifactErr, "toolkit", command)

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			applyCommandExecutionShell(result, shell)
			return result, fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 检查常见错误并给出友好提示
		friendlyHint := friendlyHintFor(command, capture.Output, err, cfg.workdir)
		if friendlyHint != "" {
			result := commandExecutionFromCapture(capture)
			result.RawOutputArtifactPath = artifactPath
			if artifactErr != nil {
				result.RawOutputArtifactError = artifactErr.Error()
			}
			applyCommandExecutionShell(result, shell)
			return result, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, GetShellEnvironmentInfo())
		}
		result := commandExecutionFromCapture(capture)
		result.RawOutputArtifactPath = artifactPath
		if artifactErr != nil {
			result.RawOutputArtifactError = artifactErr.Error()
		}
		applyCommandExecutionShell(result, shell)
		return result, err
	}

	result := commandExecutionFromCapture(capture)
	result.RawOutputArtifactPath = artifactPath
	if artifactErr != nil {
		result.RawOutputArtifactError = artifactErr.Error()
	}
	applyCommandExecutionShell(result, shell)
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
		if rawCap != nil {
			value, err := extractPositiveInt(rawCap)
			if err != nil {
				return settings, fmt.Errorf("output_bytes_cap 参数无效: %w", err)
			}
			settings.outputBytesCap = value
			settings.hasOutputBytesCap = true
		}
	}

	if settings.disableOutputCap && settings.hasOutputBytesCap {
		return settings, fmt.Errorf("output_bytes_cap 不能与 disable_output_cap 同时设置")
	}

	return settings, nil
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
