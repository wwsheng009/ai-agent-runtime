package tools

import (
	"context"

	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const defaultGlobLimit = searchcore.DefaultGlobLimit

// GlobTool 文件名模式匹配工具
type GlobTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	lookPath   func(string) (string, error)
	runCommand func(context.Context, string, string, []string) ([]byte, error)
}

// NewGlobTool 创建 Glob 工具
func NewGlobTool() *GlobTool {
	return &GlobTool{
		BaseTool: toolkit.NewBaseTool(
			"glob",
			"文件名/路径模式匹配搜索，不搜索文件内容。支持 * ? [] **；常见 shell brace（*.{go,ts}）会自动展开。结果按 mtime 从新到旧排序，可用 offset/limit 分页（next_offset 续读）。递归文件匹配优先用 rg --files；目录匹配、单层匹配或 rg 不可用时回退内置遍历；20s 预算内未完成会返回已收集的部分结果（timed_out=true）。大小写不确定时用 case_insensitive=true。",
			"1.0.0",
			searchcore.GlobParameters(),
			true,
		),
		lookPath:   runtimeripgrep.LookPath,
		runCommand: runGrepCommand,
	}
}

func (g *GlobTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindSearch,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

// Execute runs the shared core handler. Glob has exactly one implementation:
// the local surface supplies its sandbox through CoreRuntime and the remote
// surface supplies its workspace, so both execute the same code.
func (g *GlobTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return executeCoreSearchTool(ctx, g, params)
}
