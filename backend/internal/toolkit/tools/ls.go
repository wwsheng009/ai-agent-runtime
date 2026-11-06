package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// LsTool lists directory entries. The model-facing canonical name is
// "list_dir"; "ls" is kept as a compatibility alias by the tool registry so
// shell `ls` and the toolkit tool cannot be confused.
//
// It has no implementation of its own: Execute delegates to the shared lscore
// handler, which is the same code the remote surface runs. Only the sandbox and
// the schema live here, because those are the parts a surface actually owns.
type LsTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewLsTool 创建 Ls 工具
func NewLsTool() *LsTool {
	return &LsTool{
		BaseTool: toolkit.NewBaseTool(
			"list_dir",
			"列出目录内容。优先用合适的 depth 一次取得所需子树；多个独立目录可在同一轮并行调用。",
			"1.0.0",
			lscore.Parameters(),
			true,
		),
	}
}

func (l *LsTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

// Execute runs the shared lscore handler: ls has exactly one implementation, and
// the local surface contributes only its sandbox through CoreRuntime.
func (l *LsTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return executeCoreSearchTool(ctx, l, params)
}
