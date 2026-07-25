package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// ShellTool is the Codex-aligned primary shell surface. It reuses BashTool
// execution (batching, soft non-zero exits, Windows guidance) under the name
// "shell", while bash/execute_shell_command remain compatibility aliases.
type ShellTool struct {
	*BashTool
	description string
	version     string
	parameters  map[string]interface{}
}

// NewShellTool creates the preferred shell tool exposed to models.
func NewShellTool() *ShellTool {
	base := NewBashTool()
	// Prefer the full bash schema (including commands batching) under the
	// clearer "shell" name. Description emphasizes detected user shell and
	// content-success for non-zero process exits.
	description := "通过检测到的用户 shell（Windows: pwsh/powershell/cmd；Unix: $SHELL/zsh/bash/sh）执行一条命令，或用 commands 批量执行并一次返回全部结果。进程非零退出码会作为内容结果返回（含 Exit code/Output），不是工具崩溃；仅未启动/超时/取消/权限拒绝等才是硬失败。代码搜索优先用 toolkit `grep`；文件系统查看优先 ls/glob/view。Windows 默认 PowerShell，没有 head 时用 Select-Object；不要使用 bash heredoc（<<EOF）。兼容别名: bash、execute_shell_command。"
	return &ShellTool{
		BashTool:    base,
		description: description,
		version:     "1.0.0",
		parameters:  cloneShellParameters(base.Parameters()),
	}
}

// Name returns the primary tool name.
func (t *ShellTool) Name() string {
	return "shell"
}

// Description returns the tool description.
func (t *ShellTool) Description() string {
	return t.description
}

// Version returns the tool version.
func (t *ShellTool) Version() string {
	return t.version
}

// Parameters returns the JSON schema for tool parameters.
func (t *ShellTool) Parameters() map[string]interface{} {
	return t.parameters
}

// CanDirectCall indicates the tool can be invoked directly.
func (t *ShellTool) CanDirectCall() bool {
	return true
}

// Execute delegates to the underlying BashTool.
func (t *ShellTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return t.BashTool.Execute(ctx, params)
}

func cloneShellParameters(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	// Shallow clone is enough for registry/schema consumers; nested maps are
	// treated as immutable schema tables.
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
