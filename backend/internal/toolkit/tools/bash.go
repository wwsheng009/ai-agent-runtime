package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
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
				"description": "Shell 命令。Windows: 'dir' 查看目录；Linux/Mac: 'ls -la' 查看目录",
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

// Execute 实现 Tool 接口
func (b *BashTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	command, ok := params["command"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("command 参数缺失或类型错误"),
		}, nil
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("command 参数为空"),
		}, nil
	}
	mutatedPaths := extractStringList(params["mutated_paths"])

	// 检查黑名单
	if b.isBlacklisted(command) {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("命令被禁止执行（安全限制）"),
		}, nil
	}

	// 使用 executer 执行命令
	output, err := b.executeCommand(ctx, command)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Content: output,
			Error:   err,
		}, nil
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: output,
		Metadata: map[string]interface{}{
			"command":     command,
			"output_size": len(output),
			"executed_at": time.Now().Unix(),
			"mutated_paths": func() []string {
				if len(mutatedPaths) == 0 {
					return nil
				}
				return mutatedPaths
			}(),
		},
	}, nil
}

func (b *BashTool) executeCommand(ctx context.Context, command string) (string, error) {
	if b.sandbox == nil {
		return b.executer.Execute(ctx, command, b.timeout)
	}

	mainCmd := extractPrimaryCommand(command)
	if err := b.sandbox.ValidateCommand(mainCmd); err != nil {
		return "", wrapSandboxPermissionError("sandbox denied command execution", err, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
		})
	}

	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取当前工作目录失败: %w", err)
	}
	if err := b.sandbox.CheckPermission(runtimeexecutor.OpExecute, workDir); err != nil {
		return "", wrapSandboxPermissionError("sandbox denied command working directory", err, map[string]interface{}{
			"policy":      "sandbox",
			"operation":   string(runtimeexecutor.OpExecute),
			"target_path": workDir,
			"command":     mainCmd,
		})
	}

	timeout := b.timeout
	if configured := b.sandbox.Config().MaxExecutionTime; configured > 0 {
		timeout = configured
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var shellCmd []string
	if runtime.GOOS == "windows" {
		shellCmd = []string{"cmd", "/c", command}
	} else {
		shellCmd = []string{"sh", "-c", command}
	}
	if err := b.sandbox.CheckCommandDenied(shellCmd[0]); err != nil {
		return "", wrapSandboxPermissionError("sandbox denied shell launcher", err, map[string]interface{}{
			"policy":    "sandbox",
			"operation": string(runtimeexecutor.OpExecute),
			"command":   mainCmd,
			"launcher":  shellCmd[0],
		})
	}

	cmd := exec.CommandContext(cmdCtx, shellCmd[0], shellCmd[1:]...)
	cmd.Dir = workDir
	cmd.Env = b.sandbox.FilterEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return string(output), fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}
		return string(output), err
	}
	return string(output), nil
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

// isBlacklisted 检查命令是否在黑名单中
func (b *BashTool) isBlacklisted(command string) bool {
	cmdLower := strings.ToLower(command)
	for _, blocked := range b.blacklist {
		if strings.Contains(cmdLower, strings.ToLower(blocked)) {
			return true
		}
	}
	return false
}

func extractPrimaryCommand(command string) string {
	parts := strings.Fields(strings.TrimSpace(command))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// CommandExecuter 命令执行器接口
type CommandExecuter interface {
	Execute(ctx context.Context, command string, timeout time.Duration) (string, error)
}

// DefaultCommandExecuter 默认命令执行器
type DefaultCommandExecuter struct{}

// Execute 实现命令执行
func (e *DefaultCommandExecuter) Execute(ctx context.Context, command string, timeout time.Duration) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var shellCmd []string
	if runtime.GOOS == "windows" {
		shellCmd = []string{"cmd", "/c", command}
	} else {
		shellCmd = []string{"sh", "-c", command}
	}

	cmd := exec.CommandContext(cmdCtx, shellCmd[0], shellCmd[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return string(output), fmt.Errorf("命令执行超时（超过 %v）", timeout)
		}

		// 检查常见错误并给出友好提示
		cmdLower := strings.ToLower(command)
		var friendlyHint string
		mainCmd := ""
		cmdParts := strings.Fields(command)
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
		}

		if friendlyHint != "" {
			return string(output), fmt.Errorf("命令执行失败: %w\n%s", err, friendlyHint)
		}
		return string(output), err
	}

	return string(output), nil
}
