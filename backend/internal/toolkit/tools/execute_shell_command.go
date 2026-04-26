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
				"description": "要执行的 shell 命令。系统会自动检测可用 shell（Windows 通常是 PowerShell/pwsh，回退到 cmd）。切换目录优先使用 workdir 参数；查看当前目录用 pwd/Get-Location（PowerShell/pwsh）或 cd/echo %cd%（cmd），不要用裸 cd 来验证目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。搜索文件或代码时，推荐使用 grep 工具（内部已优先使用 ripgrep/rg，不可用时回退到内置扫描；grep 兼容常见 rg 风格参数与 rg_args，用法可按 ripgrep 思路迁移，例如 rg -g 'src/**/*.go' -e foo -e bar backend docs 可改为 grep 的 rg_args/patterns + paths 调用，rg --iglob '*.go' foo backend 可改为 grep 的 rg_args 或 glob 调用，rg --glob-case-insensitive -g '*.go' foo backend 可改为 grep 的 glob_case_insensitive + glob 或对应 rg_args 调用，rg -f patterns.txt backend 可改为 grep 的 pattern_file/pattern_files 或对应 rg_args 调用，rg --column --trim foo backend 可改为 grep 的 column + trim 调用，rg --sort path foo backend 或 rg --sort-files foo backend 可改为 grep 的 sort/sort_files 或对应 rg_args 调用，rg --count-matches foo backend 可改为 grep 的 count_matches 或对应 rg_args 调用，rg --stats foo backend 可改为 grep 的 stats 或对应 rg_args 调用，rg --json foo backend 可改为 grep 的 json/json_output 或对应 rg_args 调用，rg --follow foo backend / rg -L foo backend 可改为 grep 的 follow 或对应 rg_args 调用，rg -P 'foo.*bar' backend、rg -U 'foo\\nbar' backend、rg -r X foo backend 这类 pcre2/engine/multiline/multiline_dotall/replace/passthru/crlf/auto_hybrid_regex 参数在有 rg 时也可通过 grep 的 rg_args 透传，也支持直接使用结构化参数 pcre2/engine/multiline/multiline_dotall/replace/passthru/crlf/auto_hybrid_regex；rg -M 80 foo backend / rg --max-columns 80 foo backend 可改为 grep 的 max_columns + max_columns_preview / no_max_columns_preview 调用，rg --pretty backend 可改为 grep 的 pretty 调用，rg --line-buffered / --no-line-buffered 可改为 grep 的 line_buffered / no_line_buffered 调用，rg -o foo path/file.txt 可改为 grep 的 only_matching + 单文件 path 调用，rg --files-without-match foo backend 可改为 grep 的 files_without_match 或对应 rg_args 调用，rg --max-filesize 1M foo backend 可改为 grep 的 max_filesize 调用，rg --max-depth 0 -m 0 foo backend 这类显式 0 语义也已兼容，常见短参数组合如 -iwg*.go 也可按 rg_args 心智迁移；grep 结果会稳定规范化为 path:line[:column]: content，因此 -n/-H/-N/--no-filename/--color 以及 line_number/with_filename/no_filename/color 这类展示层参数即使传入也不会改变输出骨架；若请求 json/--json，则会按 rg 的 JSON Lines 事件流透传输出），而非通过 shell 直接调用 rg/grep。路径建议使用正斜杠（如 E:/projects/foo）。",
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
		description: "在指定工作目录执行 shell 命令并返回输出结果。系统会自动检测最优 shell（Windows: PowerShell Core > PowerShell > cmd；Unix: $SHELL > zsh > bash > sh）。切换目录优先使用 workdir 参数；不要用裸 cd 验证当前目录。Windows PowerShell/pwsh 默认没有 `head`，需要截断输出时请改用 `Select-Object -First N`。搜索文件或代码时推荐使用 grep 工具（内部已优先使用 ripgrep/rg，不可用时回退到内置扫描；grep 兼容常见 rg 风格参数与 rg_args，用法可按 ripgrep 思路迁移，例如 rg -g 'src/**/*.go' -e foo -e bar backend docs 可改为 grep 的 rg_args/patterns + paths 调用，rg --iglob '*.go' foo backend 可改为 grep 的 rg_args 或 glob 调用，rg --glob-case-insensitive -g '*.go' foo backend 可改为 grep 的 glob_case_insensitive + glob 或对应 rg_args 调用，rg -f patterns.txt backend 可改为 grep 的 pattern_file/pattern_files 或对应 rg_args 调用，rg --column --trim foo backend 可改为 grep 的 column + trim 调用，rg --sort path foo backend 或 rg --sort-files foo backend 可改为 grep 的 sort/sort_files 或对应 rg_args 调用，rg --count-matches foo backend 可改为 grep 的 count_matches 或对应 rg_args 调用，rg --stats foo backend 可改为 grep 的 stats 或对应 rg_args 调用，rg --json foo backend 可改为 grep 的 json/json_output 或对应 rg_args 调用，rg --follow foo backend / rg -L foo backend 可改为 grep 的 follow 或对应 rg_args 调用，rg -P 'foo.*bar' backend、rg -U 'foo\\nbar' backend、rg -r X foo backend 这类 pcre2/engine/multiline/multiline_dotall/replace/passthru/crlf/auto_hybrid_regex 参数在有 rg 时也可通过 grep 的 rg_args 透传，也支持直接使用结构化参数 pcre2/engine/multiline/multiline_dotall/replace/passthru/crlf/auto_hybrid_regex；rg -M 80 foo backend / rg --max-columns 80 foo backend 可改为 grep 的 max_columns + max_columns_preview / no_max_columns_preview 调用，rg --pretty backend 可改为 grep 的 pretty 调用，rg --line-buffered / --no-line-buffered 可改为 grep 的 line_buffered / no_line_buffered 调用，rg -o foo path/file.txt 可改为 grep 的 only_matching + 单文件 path 调用，rg --files-without-match foo backend 可改为 grep 的 files_without_match 或对应 rg_args 调用，rg --max-filesize 1M foo backend 可改为 grep 的 max_filesize 调用，rg --max-depth 0 -m 0 foo backend 这类显式 0 语义也已兼容，常见短参数组合如 -iwg*.go 也可按 rg_args 心智迁移；grep 结果会稳定规范化为 path:line[:column]: content，因此 -n/-H/-N/--no-filename/--color 以及 line_number/with_filename/no_filename/color 这类展示层参数即使传入也不会改变输出骨架；若请求 json/--json，则会按 rg 的 JSON Lines 事件流透传输出），而非通过 shell 直接调用 rg/grep。路径建议使用正斜杠格式（如 E:/projects/foo）。适用于查看文件、目录、系统信息等场景。",
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
