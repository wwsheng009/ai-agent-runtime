package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ExecuteShellCommandTool is a compatibility wrapper around BashTool using the legacy name.
type ExecuteShellCommandTool struct {
	*BashTool
	description string
	version     string
	parameters  map[string]interface{}
}

// NewExecuteShellCommandTool creates a tool compatible with execute_shell_command.
func NewExecuteShellCommandTool() *ExecuteShellCommandTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）。切换目录优先使用 workdir 参数；查看当前目录用 pwd/Get-Location（PowerShell/pwsh）或 cd/echo %cd%（cmd），不要用裸 cd 来验证目录。路径建议使用正斜杠（如 E:/projects/foo）。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。路径请使用正斜杠（如 E:/projects/foo）以兼容所有平台。",
			},
			"mutated_paths": map[string]interface{}{
				"type":        "array",
				"description": "可选：命令将修改的文件路径列表，用于变更追踪与回滚。",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	}

	return &ExecuteShellCommandTool{
		BashTool:    NewBashTool(),
		description: "在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。切换目录优先使用 workdir 参数；不要用裸 cd 验证当前目录。路径建议使用正斜杠格式（如 E:/projects/foo）。适用于查看文件、目录、系统信息等场景。",
		version:     "1.1.0",
		parameters:  parameters,
	}
}

// Name returns the tool name.
func (t *ExecuteShellCommandTool) Name() string {
	return "execute_shell_command"
}

// Description returns the tool description.
func (t *ExecuteShellCommandTool) Description() string {
	return t.description
}

// Version returns the tool version.
func (t *ExecuteShellCommandTool) Version() string {
	return t.version
}

// Parameters returns the JSON schema for tool parameters.
func (t *ExecuteShellCommandTool) Parameters() map[string]interface{} {
	return t.parameters
}

// CanDirectCall indicates the tool can be invoked directly.
func (t *ExecuteShellCommandTool) CanDirectCall() bool {
	return true
}

// Execute delegates to the underlying BashTool.
func (t *ExecuteShellCommandTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return t.BashTool.Execute(ctx, params)
}
