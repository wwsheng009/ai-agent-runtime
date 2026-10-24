package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// WriteTool 文件写入工具
type WriteTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewWriteTool 创建 Write 工具
func NewWriteTool() *WriteTool {
	return &WriteTool{
		BaseTool: toolkit.NewBaseTool(
			"write",
			"创建或覆盖单个文件内容；适合写入完整文件或较小分块。若需要写入多个文件或较长内容，请拆分为多个更小的 write/edit 调用，每次只聚焦一个文件和一个写入目标，按章节或按块逐步写入，避免单次工具参数过大导致截断。",
			"1.0.0",
			mutatecore.WriteParameters(),
			true,
		),
	}
}

func (w *WriteTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassIdempotencyKeyRequired,
		// file_path is a local write target: the leaf may legitimately not exist
		// yet, so the existence preflight must never deny it (P1-4 path roles).
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleOutput,
		},
	}
}

// Execute runs the shared mutatecore handler. The write pipeline - encoding
// preservation, atomic replace, binary refusal, the document-container guard,
// the inline-payload limit and the expected_sha256 precondition - has exactly
// one implementation. Host-scoped state (the read-before-write ledger and the
// change notifier) is fed through toolcore.MutationObserver.
func (w *WriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}
	return executeCoreSearchTool(ctx, w, params)
}
