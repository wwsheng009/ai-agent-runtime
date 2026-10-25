package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// artifactReadHeaderReserveBytes covers the window header, tool_call_id and
	// separator that wrap every page, so the payload stays inside the tool's own
	// budget (artifactOutputBudgetBytes).
	artifactReadHeaderReserveBytes = 512
	// artifactReadMinLimitBytes is the floor used when the configured budget is
	// unusually small; the tool still returns a useful window.
	artifactReadMinLimitBytes = 1024
)

// ArtifactReadTool 读取归档的工具结果原文（`Full raw output artifact_id: art_…` 指针的目标）。
type ArtifactReadTool struct {
	*toolkit.BaseTool
}

// NewArtifactReadTool 创建 artifact_read 工具。
func NewArtifactReadTool() *ArtifactReadTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"artifact_id": map[string]interface{}{
				"type":        "string",
				"description": "归档记录 id（art_<32位hex>），来自工具结果末尾的 “Full raw output artifact_id: art_…” 指针行。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "0-based 字节偏移，默认为 0。续读时传入上一次返回的 next_offset。",
			},
			"limit": map[string]interface{}{
				"type": "integer",
				"description": fmt.Sprintf(
					"本次最多返回的原始字节数，默认与单次上限一致（当前 %d 字节）；超出模型可见上限会被收敛。单次读取不会再次触发指针替换。",
					artifactReadDefaultLimitBytes(),
				),
			},
		},
		"required": []string{"artifact_id"},
	}

	return &ArtifactReadTool{
		BaseTool: toolkit.NewBaseTool(
			"artifact_read",
			"按 id 读取工具结果归档中的完整原始输出。当工具结果显示 “Full raw output artifact_id: art_…” 时，用本工具取回被截断窗口之外的原文；offset/limit 为字节窗口，可分段续读直到 eof=true。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

// DefinitionMetadata 声明只读语义，使只读子代理仍可解引用自己的 artifact 指针。
func (a *ArtifactReadTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

// ArtifactReadParams 是 artifact_read 的入参。
type ArtifactReadParams struct {
	ArtifactID string `json:"artifact_id"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

// Execute 实现 Tool 接口。
func (a *ArtifactReadTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p ArtifactReadParams
	encoded, err := json.Marshal(params)
	if err != nil || json.Unmarshal(encoded, &p) != nil {
		return artifactReadFailure("artifact_read 参数格式无效"), nil
	}

	// The miss metric for a call that names no id at all is recorded here: the
	// shared handler owns the rejection now and it has no metric sink, while every
	// store/scope miss is reported by the sink itself.
	if strings.TrimSpace(p.ArtifactID) == "" {
		observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonBadArgs)
	}
	result, err := toolcore.NewArtifactReadHandler(a.coreDefinition()).Execute(ctx, &toolcore.Call{
		Args:    params,
		Runtime: a.CoreRuntime(ctx),
	})
	if err != nil {
		return nil, err
	}
	return localizeErrorCode(toolkit.ResultToToolkit(result)), nil
}

// coreDefinition is the shared handler's view of this tool.
func (a *ArtifactReadTool) coreDefinition() toolcore.Definition {
	return toolcore.Definition{
		Name:                "artifact_read",
		Version:             a.Version(),
		DefaultOutputBudget: artifactOutputBudgetBytes,
	}
}

// CoreRuntime binds the session's artifact store as a toolcore.Runtime, so the
// shared artifact_read handler can run locally with the same scope rules the
// local tool has always enforced.
func (a *ArtifactReadTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	runtime := newLocalCoreRuntimeWith(ctx, nil, nil)
	if local, ok := runtime.(*localCoreRuntime); ok {
		runtime := localArtifactSink{ctx: ctx}
		local.artifactSink = runtime
	}
	return runtime
}

// artifactReadDefaultLimitBytes resolves the default page size: the full
// max-window cap, so a budget-sized artifact is dereferenced in a single read.
func artifactReadDefaultLimitBytes() int {
	return artifactReadMaxLimitBytes()
}

// artifactReadMaxLimitBytes resolves the byte cap for a single window from the
// tool's own budget. Every result stamps skip_render_truncation, so a page can
// never grow a second artifact pointer regardless of the render-layer budget.
func artifactReadMaxLimitBytes() int {
	limit := artifactOutputBudgetBytes - artifactReadHeaderReserveBytes
	if limit < artifactReadMinLimitBytes {
		limit = artifactReadMinLimitBytes
	}
	return limit
}

// artifactRuneStartAtOrBefore snaps a byte index back to the nearest UTF-8 rune
// boundary so windows never split a multi-byte character.
func artifactRuneStartAtOrBefore(content string, index int) int {
	if index <= 0 {
		return 0
	}
	if index >= len(content) {
		return len(content)
	}
	for index > 0 && !utf8.RuneStart(content[index]) {
		index--
	}
	return index
}

func artifactReadFailure(message string) *toolkit.ToolResult {
	// Failures page nothing, but they must still own their output: an error
	// body folded by the render layer would hide the reason the read failed
	// behind a truncation notice.
	return stampToolOwnsOutputWithBudget(&toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      fmt.Errorf("%s", message),
	}, artifactOutputBudgetBytes)
}
