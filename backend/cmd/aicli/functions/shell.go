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

// CommandExecuter 命令执行器接口（用于测试和自定义）
type CommandExecuter interface {
	Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error)
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

// Execute 执行命令
func (e *DefaultCommandExecuter) Execute(ctx context.Context, command string, timeout time.Duration, opts ...ExecOption) (string, error) {
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
	capture, err := runtimeexecutor.CaptureCombinedOutput(cmd, runtimeexecutor.DefaultRetainedOutputBytes)
	outputStr := capture.Output

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return outputStr, fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 针对常见错误给出友好提示
		friendlyHint := friendlyHintForCommand(command, outputStr, err)
		if friendlyHint != "" {
			return outputStr, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, getShellEnvironmentInfo())
		}
		return outputStr, fmt.Errorf("命令执行失败: %w\n\n当前环境信息:\n%s", err, getShellEnvironmentInfo())
	}

	return outputStr, nil
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
	return "在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。切换目录优先使用 workdir 参数；不要用裸 cd 验证当前目录。路径建议使用正斜杠格式（如 E:/projects/foo）以确保跨平台兼容。"
}

// Parameters 返回 Function 参数的 JSON Schema 描述
func (f *ShellFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）。切换目录优先使用 workdir 参数；查看当前目录用 pwd/Get-Location（PowerShell/pwsh）或 cd/echo %cd%（cmd），不要用裸 cd 来验证目录。路径请使用正斜杠（如 E:/projects/foo）。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。路径请使用正斜杠（如 E:/projects/foo）以兼容所有平台。",
			},
		},
		"required": []string{"command"},
	}
}

// Execute 执行 Function
func (f *ShellFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("command 参数缺失或不是字符串，args: %+v, command值: %v (类型: %T)", args, args["command"], args["command"])
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command 参数为空")
	}

	// 解析可选的 workdir
	var opts []ExecOption
	if workdir, ok := args["workdir"].(string); ok && strings.TrimSpace(workdir) != "" {
		opts = append(opts, WithWorkdir(strings.TrimSpace(workdir)))
	}

	// 执行命令，30秒超时
	return f.executer.Execute(ctx, command, 30*time.Second, opts...)
}

// SetExecuter 设置命令执行器（用于测试）
func (f *ShellFunction) SetExecuter(executer CommandExecuter) {
	f.executer = executer
}
