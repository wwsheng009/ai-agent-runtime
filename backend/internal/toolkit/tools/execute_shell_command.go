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
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）；切换目录优先使用 workdir 参数，查看当前目录用 Get-Location，不要用裸 cd；Windows 没有 head，截断输出用 Select-Object -First N。",
			},
			"workdir": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令执行的工作目录。绝对路径直接使用，相对路径基于当前工作目录解析。默认为当前工作目录。路径请使用正斜杠（如 E:/projects/foo）以兼容所有平台。",
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "可选：命令超时，值必须是带引号的 JSON 字符串，例如 \"30s\"、\"2m\"、\"5m\"（裸写 30s 会让整个 arguments 变成非法 JSON）。默认 30s，可用 AICLI_SHELL_COMMAND_TIMEOUT 或 AICLI_SHELL_COMMAND_TIMEOUT_MS 调整全局默认；运行测试、构建、类型检查等可能超过默认值的命令时，应由模型显式设置更长超时。",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时毫秒数。小于 100 的数值会视为模型单位混淆并忽略；确需亚 100ms 时使用 timeout 字符串（如 \"30ms\"）。秒级超时优先只设 timeout_sec 或 timeout。",
			},
			"timeout_sec": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：命令超时秒数，必须为正整数。优先级低于 timeout_ms，高于 timeout。与 timeout_ms 二选一即可，不要同时填占位 1ms。",
			},
			"output_bytes_cap": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "可选：stdout/stderr 合并输出的保留上限（字节）。用于覆盖默认 256KB capture limit；必须为正整数。若同时设置 disable_output_cap=true，为保证资源边界，以本参数为准。",
			},
			"disable_output_cap": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：设为 true 时关闭 shell 输出 capture limit，尽量保留完整原始输出；若同时设置 output_bytes_cap，则保留后者的显式上限。",
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
		description: "兼容别名：优先使用 `shell`（或 `bash`）。在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。进程非零退出码会作为内容结果返回（含 Exit code/Output），不是工具崩溃；仅未启动/超时/取消/权限拒绝等才是硬失败。切换目录优先使用 workdir 参数，不要用裸 cd 验证当前目录；Windows PowerShell/pwsh 默认没有 `head`，截断输出请用 `Select-Object -First N`。搜索文件或代码请优先使用 grep 工具；rg→grep 参数迁移与 Windows 细节见 docs/tools/shell-grep-migration.md。",
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
