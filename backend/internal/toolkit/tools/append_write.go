package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AppendWriteTool appends one chunk of text to a file, with optional truncate-first
// behavior for the first chunk of a long generation.
type AppendWriteTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewAppendWriteTool creates a chunk-friendly append tool for long text writes.
func NewAppendWriteTool() *AppendWriteTool {
	return &AppendWriteTool{
		BaseTool: toolkit.NewBaseTool(
			"append_write",
			"按块追加写入文件；适合长文本、长代码、章节内容等分块落盘。第一块可用 truncate_first=true，后续块继续调用 append_write，避免单次 write(content=全文) 导致工具参数过大或输出截断。",
			"1.0.0",
			mutatecore.AppendParameters(),
			true,
		),
	}
}

func (w *AppendWriteTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassIdempotencyKeyRequired,
		// file_path is a local write target that may not exist yet (P1-4).
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleOutput,
		},
	}
}

// Execute runs the shared mutatecore handler. Every chunking rule - the first
// chunk overwrite, the trailing-newline option, the expected_offset replay and
// precondition - has one implementation, so a remote caller gets the same
// behavior as a local one.
func (w *AppendWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}
	return executeCoreSearchTool(ctx, w, params)
}
