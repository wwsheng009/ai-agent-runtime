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
				"description": "要执行的 shell 命令。注意命令需与当前操作系统匹配。Windows: 'dir'查看目录，'type filename.txt'查看文件，'systeminfo'查看系统信息，'ver'查看系统版本；Linux/Mac: 'ls -la'查看目录，'cat filename.txt'查看文件，'uname -a'查看系统信息，'pwd'查看当前目录，'df -h'查看磁盘空间等",
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
		description: "在当前工作目录执行 shell 命令并返回输出结果。适用于查看文件、目录、系统信息等场景。请根据命令的实际用途选择合适的命令，如查看系统信息（Windows: cmd命令；Linux/Mac: uname/ls等）。",
		version:     "1.0.0",
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
