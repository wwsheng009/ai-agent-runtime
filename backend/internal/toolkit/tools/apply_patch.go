package tools

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// applyPatchLarkGrammar is the freeform grammar the model is shown. It is
// metadata, not a second parser: the grammar that actually runs lives once, in
// mutatecore.ParsePatch.
const applyPatchLarkGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change_move: "*** Move to: " filename LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF`

// ApplyPatchTool applies Codex-style patch payloads directly to workspace files.
type ApplyPatchTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewApplyPatchTool creates a real apply_patch tool for workspace edits.
func NewApplyPatchTool() *ApplyPatchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{
				"type":        "string",
				"description": "要应用的补丁文本。普通函数调用模式下放入 patch 参数；Codex/freeform 模式下直接发送补丁文本，不要包 JSON。必须使用 Codex apply_patch 格式，例如 *** Begin Patch / *** Update File / *** Add File / *** Delete File / *** End Patch。若补丁很大或包含多个独立变更，请拆分为多个更小的补丁块，每次只聚焦一个文件或一个变更区域，避免单次参数过长导致截断。",
			},
		},
		"required": []string{"patch"},
	}

	return &ApplyPatchTool{
		BaseTool: toolkit.NewBaseTool(
			"apply_patch",
			"应用 Codex 风格补丁到工作区文件，支持新增、更新、删除和重命名文件。Codex/custom-tool 模式下这是 FREEFORM 工具，应直接发送补丁文本，不要包 JSON；普通函数调用模式使用 patch 参数。若需要处理很大的变更或多个独立目标，请拆分为多个更小的 apply_patch 调用，每次只聚焦一个文件或一个变更区域，避免单次参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

func (t *ApplyPatchTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		"freeform": map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": applyPatchLarkGrammar,
		},
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
	}
}

// Execute implements the Tool interface.
func (t *ApplyPatchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}
	// The freeform tool call arrives either as a `patch` argument or as raw
	// text; normalize that at the surface, then run the shared handler.
	if raw, ok := params["patch"].(string); !ok || strings.TrimSpace(raw) == "" {
		if fallback, ok := params["_raw"].(string); ok && strings.TrimSpace(fallback) != "" {
			copied := make(map[string]interface{}, len(params)+1)
			for key, value := range params {
				copied[key] = value
			}
			copied["patch"] = fallback
			params = copied
		}
	}
	return executeCoreSearchTool(ctx, t, params)
}
