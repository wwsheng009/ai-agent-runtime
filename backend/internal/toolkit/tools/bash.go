package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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

// NewBashTool 创建 Bash 工具
func NewBashTool() *BashTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command. On Windows this tool uses the detected user shell, usually PowerShell/pwsh rather than cmd.exe. Prefer the workdir parameter for directory changes; use pwd/Get-Location to print the current directory and do not use bare cd for that purpose.",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。",
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
			"执行 Shell 命令并返回输出",
			"1.0.0",
			parameters,
			true, // 支持直接调用
		),
		executer:  &DefaultCommandExecuter{},
		timeout:   30 * time.Second,
		blacklist: defaultBlacklist,
	}
}

type CommandExecutionResult struct {
	Output     string
	Truncated  bool
	TotalBytes int
	TotalLines int
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

	// 检查黑名单
	if b.isBlacklisted(command) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("命令被禁止执行（安全限制）"),
		}, nil
	}

	// 使用 executer 执行命令
	execResult, err := b.executeCommand(ctx, command, workdir)
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

func (b *BashTool) executeCommand(ctx context.Context, command string, workdir string) (CommandExecutionResult, error) {
	// 解析工作目录
	resolvedWorkdir, err := resolveWorkdir(workdir)
	if err != nil {
		return CommandExecutionResult{}, err
	}

	if b.sandbox == nil {
		return b.executer.Execute(ctx, command, b.timeout, WithWorkdir(resolvedWorkdir))
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

	capture, err := runtimeexecutor.CaptureCombinedOutput(cmd, runtimeexecutor.DefaultRetainedOutputBytes)
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return commandExecutionFromCapture(capture), fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}
		return commandExecutionFromCapture(capture), err
	}
	return commandExecutionFromCapture(capture), nil
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
	workdir string
}

// WithWorkdir sets the working directory for command execution.
func WithWorkdir(dir string) ExecOption {
	return func(c *execConfig) {
		c.workdir = dir
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

	capture, err := runtimeexecutor.CaptureCombinedOutput(cmd, runtimeexecutor.DefaultRetainedOutputBytes)

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return commandExecutionFromCapture(capture), fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 检查常见错误并给出友好提示
		friendlyHint := friendlyHintFor(command, capture.Output, err)
		if friendlyHint != "" {
			return commandExecutionFromCapture(capture), fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, GetShellEnvironmentInfo())
		}
		return commandExecutionFromCapture(capture), err
	}

	return commandExecutionFromCapture(capture), nil
}

// --- Helper functions ---

func commandExecutionFromCapture(capture runtimeexecutor.CombinedOutputCapture) CommandExecutionResult {
	return CommandExecutionResult{
		Output:     capture.Output,
		Truncated:  capture.Truncated,
		TotalBytes: capture.TotalBytes,
		TotalLines: capture.TotalLines,
	}
}

func buildCommandExecutionMetadata(command string, mutatedPaths []string, result CommandExecutionResult) map[string]interface{} {
	metadata := map[string]interface{}{
		"command":               command,
		"output_size":           len(result.Output),
		"captured_output_bytes": len(result.Output),
		"total_output_bytes":    result.TotalBytes,
		"total_output_lines":    result.TotalLines,
		"output_truncated":      result.Truncated,
		"executed_at":           time.Now().Unix(),
	}
	if len(mutatedPaths) > 0 {
		metadata["mutated_paths"] = mutatedPaths
	}
	return metadata
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
func friendlyHintFor(command string, output string, err error) string {
	cmdLower := strings.ToLower(command)
	mainCmd := ""
	cmdParts := strings.Fields(command)
	if len(cmdParts) > 0 {
		mainCmd = strings.ToLower(cmdParts[0])
	}

	exitCode := -1
	if exitError, ok := err.(*exec.ExitError); ok {
		exitCode = exitError.ExitCode()
	}

	switch {
	case cmdLower == "pwd" && runtime.GOOS == "windows":
		shell := runtimeexecutor.DefaultUserShell()
		if shell.Type == runtimeexecutor.ShellTypeCmd {
			return "提示: cmd.exe 下请使用 `cd` 或 `echo %cd%` 查看当前目录；PowerShell/pwsh 下请使用 `pwd` 或 `Get-Location`。"
		}
		return ""
	case mainCmd == "ls" && runtime.GOOS == "windows":
		return "提示: Windows 下请使用 `dir` 查看目录内容"
	case mainCmd == "uname" && runtime.GOOS == "windows":
		return "提示: Windows 下请使用 `ver` 或 `systeminfo` 查看系统信息"
	case mainCmd == "cat" && runtime.GOOS == "windows" && !strings.Contains(cmdLower, "."):
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

// GetShellEnvironmentInfo returns environment info for error messages,
// including the detected shell type.
func GetShellEnvironmentInfo() string {
	shell := runtimeexecutor.DefaultUserShell()
	var parts []string
	parts = append(parts, fmt.Sprintf("系统类型: %s", runtime.GOOS))
	parts = append(parts, fmt.Sprintf("Shell: %s (%s)", shell.Type, shell.Path))
	return strings.Join(parts, "\n")
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
	parts := strings.Fields(strings.TrimSpace(command))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
