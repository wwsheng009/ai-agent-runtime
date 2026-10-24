package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// EditTool 文件编辑工具（单处替换）
type EditTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	// insideGitWorkTree 探测目录是否位于 git 工作树内（测试可替换以固定判定）。
	insideGitWorkTree func(dir string) bool
}

// NewEditTool 创建 Edit 工具
func NewEditTool() *EditTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要修改的文件绝对路径。若需要改写多个文件，请拆分为多次 edit 调用，每次只聚焦一个文件和一个替换目标。",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "要替换的文本（必须精确匹配，包括空格和换行）。edit 不做模糊匹配；仅适合刚通过 view/grep 确认存在的小片段。代码编辑、多行替换或上下文可能漂移时优先使用 apply_patch。若需要替换很长的片段或多个位置，请拆分为更小的定位块，避免单次参数过长导致工具调用被截断。",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "替换后的文本。若新内容较长，请拆分为多个更小的 edit/write 调用，每次只聚焦一个替换目标，按块逐步替换或重建。",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "是否替换所有匹配项（默认为 false）。非 replace_all 且 old_string 精确命中多处时会拒绝并回报命中数量/行号，不再静默替换第一处；与 replacement_count 互斥。",
			},
			"replacement_count": map[string]interface{}{
				"type":        "integer",
				"description": "只替换前 N 处匹配（必须 > 0）；与 replace_all 互斥。仅在同一片段多次出现且确实要改动前 N 处时使用。",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}

	return &EditTool{
		BaseTool: toolkit.NewBaseTool(
			"edit",
			"编辑单个文件：使用 new_string 替换文件中的 old_string；只适合刚确认存在的小范围精确替换，不做语义模糊改写。old_string 命中多处且未显式指定 replace_all/replacement_count 时会拒绝并回报数量与行号。代码编辑、多行替换或上下文可能变化时优先使用 apply_patch。若要改写多个文件或大段内容，请拆分为多个更小的 edit/write 调用，每次只聚焦一个文件和一个替换目标，按章节或按块逐步处理，避免单次参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
		insideGitWorkTree: isInsideGitWorkTree,
	}
}

func (e *EditTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindEdit,
		runtimetypes.ToolMetadataReadOnlyKey:         false,
		runtimetypes.ToolMetadataMutatesFSKey:        true,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: false,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassNever,
		// file_path is read for matching and written on success: an inout target
		// that must already exist, but is still classified as a write for the
		// sandbox check (P1-4).
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleInOut,
		},
	}
}

type EditParams struct {
	FilePath         string `json:"file_path"`
	OldString        string `json:"old_string"`
	NewString        string `json:"new_string"`
	ReplaceAll       bool   `json:"replace_all,omitempty"`
	ReplacementCount int    `json:"replacement_count,omitempty"`
}

// editMaxFileBytes is the shared limit, so this surface and the core cannot
// disagree about how large an edit may be.
const editMaxFileBytes = mutatecore.EditMaxFileBytes

// Execute runs the shared mutatecore handler. The matching ladder, the
// ambiguity guard, the backup policy and the diagnostics all have exactly one
// implementation; this surface contributes its sandbox through CoreRuntime and
// its transport-level argument guard.
func (e *EditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}
	return executeCoreSearchTool(ctx, e, params)
}
