package functions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShellFunction 执行 shell 命令的 Function
type ShellFunction struct {
	executer CommandExecuter
}

// CommandExecuter 命令执行器接口（用于测试和自定义）
type CommandExecuter interface {
	Execute(ctx context.Context, command string, timeout time.Duration) (string, error)
}

// DefaultCommandExecuter 默认命令执行器
type DefaultCommandExecuter struct{}

// Execute 执行命令
func (e *DefaultCommandExecuter) Execute(ctx context.Context, command string, timeout time.Duration) (string, error) {
	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 根据操作系统选择 shell
	var shellCmd []string
	if runtime.GOOS == "windows" {
		shellCmd = []string{"cmd", "/c", command}
	} else {
		shellCmd = []string{"sh", "-c", command}
	}

	// 执行命令
	cmd := exec.CommandContext(ctx, shellCmd[0], shellCmd[1:]...)

	// 获取命令输出
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return outputStr, fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 针对常见错误给出友好提示
		cmdLower := strings.ToLower(command)
		cmdParts := strings.Fields(command)

		var friendlyHint string
		exitCode := -1
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}

		// 获取主命令名
		mainCmd := ""
		if len(cmdParts) > 0 {
			mainCmd = strings.ToLower(cmdParts[0])
		}

		switch {
		case cmdLower == "pwd" && runtime.GOOS == "windows":
			friendlyHint = "提示: Windows 下请使用 `cd` 查看当前目录，或 `echo %cd%`"

		case mainCmd == "ls" && runtime.GOOS == "windows":
			friendlyHint = "提示: Windows 下请使用 `dir` 查看目录内容"

		case mainCmd == "uname" && runtime.GOOS == "windows":
			friendlyHint = "提示: Windows 下请使用 `ver` 或 `systeminfo` 查看系统信息"

		case (mainCmd == "cat" && runtime.GOOS == "windows") && !strings.Contains(cmdLower, "."):
			friendlyHint = "提示: Windows 下请使用 `type` 查看文件内容"

		case exitCode == 127:
			friendlyHint = "提示: 命令未找到，请检查命令拼写或确认命令是否已安装"

		case strings.Contains(strings.ToLower(outputStr), "permission") ||
		     strings.Contains(strings.ToLower(outputStr), "access") && strings.Contains(strings.ToLower(outputStr), "denied"):
			friendlyHint = "提示: 权限不足，请检查是否有执行该命令的权限"

		case strings.Contains(outputStr, "no such file or directory") ||
		     strings.Contains(outputStr, "cannot find the path"):
			friendlyHint = "提示: 文件或目录不存在"
		}

		if friendlyHint != "" {
			return outputStr, fmt.Errorf("命令执行失败: %w\n%s\n\n当前环境信息:\n%s", err, friendlyHint, GetEnvironmentInfo())
		}
		return outputStr, fmt.Errorf("命令执行失败: %w\n\n当前环境信息:\n%s", err, GetEnvironmentInfo())
	}

	return outputStr, nil
}

// GetEnvironmentInfo 获取基础环境信息（用于命令执行失败时报告）
func GetEnvironmentInfo() string {
	var parts []string

	// 添加操作系统类型
	parts = append(parts, fmt.Sprintf("系统类型: %s", runtime.GOOS))

	// 添加系统版本
	if runtime.GOOS == "windows" {
		// 执行 ver 命令获取 Windows 版本
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "cmd", "/c", "ver")
		output, err := cmd.CombinedOutput()
		if err == nil && len(output) > 0 {
			parts = append(parts, fmt.Sprintf("系统版本: %s", strings.TrimSpace(string(output))))
		} else {
			parts = append(parts, "系统版本: Windows")
		}
	} else {
		// 执行 uname -a 获取 Linux/Mac 系统信息
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", "uname -a")
		output, err := cmd.CombinedOutput()
		if err == nil && len(output) > 0 {
			parts = append(parts, fmt.Sprintf("系统版本: %s", strings.TrimSpace(string(output))))
		} else {
			parts = append(parts, fmt.Sprintf("系统版本: %s", runtime.GOOS))
		}
	}

	// 添加shell类型
	if runtime.GOOS == "windows" {
		parts = append(parts, "Shell: cmd.exe (Windows命令提示符)")
	} else {
		// 尝试获取当前shell
		if shellPath := os.Getenv("SHELL"); shellPath != "" {
			shellName := ""
			if idx := strings.LastIndex(shellPath, "/"); idx > 0 {
				shellName = shellPath[idx+1:]
			} else {
				shellName = shellPath
			}
			parts = append(parts, fmt.Sprintf("Shell: %s", shellName))
		} else {
			parts = append(parts, "Shell: sh (默认)")
		}
	}

	return strings.Join(parts, "\n")
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
	return "在当前工作目录执行 shell 命令并返回输出结果。适用于查看文件、目录、系统信息等场景。请根据命令的实际用途选择合适的命令，如查看系统信息（Windows: cmd命令；Linux/Mac: uname/ls等）。"
}

// Parameters 返回 Function 参数的 JSON Schema 描述
func (f *ShellFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令。注意命令需与当前操作系统匹配。Windows: 'dir'查看目录，'type filename.txt'查看文件，'systeminfo'查看系统信息，'ver'查看系统版本；Linux/Mac: 'ls -la'查看目录，'cat filename.txt'查看文件，'uname -a'查看系统信息，'pwd'查看当前目录，'df -h'查看磁盘空间等",
			},
		},
		"required": []string{"command"},
	}
}

// Execute 执行 Function
func (f *ShellFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		// 打印详细的调试信息
		return "", fmt.Errorf("command 参数缺失或不是字符串，args: %+v, command值: %v (类型: %T)", args, args["command"], args["command"])
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command 参数为空")
	}

	// 执行命令，30秒超时
	return f.executer.Execute(ctx, command, 30*time.Second)
}

// SetExecuter 设置命令执行器（用于测试）
func (f *ShellFunction) SetExecuter(executer CommandExecuter) {
	f.executer = executer
}
