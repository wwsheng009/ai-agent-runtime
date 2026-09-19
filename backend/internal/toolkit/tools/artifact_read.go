package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// artifactReadDefaultLimitBytes keeps a single read comfortably below the
	// model-visible tool text budget so dereferencing a pointer never triggers
	// another truncation/pointer cascade.
	artifactReadDefaultLimitBytes = 8 * 1024
	// artifactReadHeaderReserveBytes reserves room for the window header plus
	// the model-history wrapper that surrounds the raw bytes.
	artifactReadHeaderReserveBytes = 512
	// artifactReadMinLimitBytes is the floor used when the configured budget is
	// unusually small; the tool still returns a useful window.
	artifactReadMinLimitBytes = 1024
)

// artifactReadIDPattern matches the artifact record id namespace
// (art_<32 hex>) anywhere in a caller-supplied string, so a model may paste the
// whole pointer tail ("art_… size=… kind=…") instead of the bare id.
var artifactReadIDPattern = regexp.MustCompile(`(?i)art_[0-9a-f]{32}`)

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
					"本次最多返回的原始字节数，默认 %d；超出模型可见上限会被收敛。单次读取不会再次触发指针替换。",
					artifactReadDefaultLimitBytes,
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

	id := normalizeArtifactReadID(p.ArtifactID)
	if id == "" {
		return artifactReadFailure("artifact_id 不能为空：请传入工具结果末尾指针行中的 art_<32位hex> id"), nil
	}

	store := toolctx.ArtifactStore(ctx)
	if store == nil {
		return artifactReadFailure("artifact store 不可用：artifact_read 只能在运行时 agent 循环内执行"), nil
	}

	record, err := store.Get(ctx, id)
	if err != nil {
		return artifactReadFailure(fmt.Sprintf("读取 artifact 失败: %v", err)), nil
	}
	if record == nil {
		return artifactReadFailure(fmt.Sprintf("artifact not found: %s", id)), nil
	}

	// Artifact 是会话内数据；指针只会出现在产生它的会话里。跨会话读取直接拒绝，
	// 避免把另一个会话的原始输出带进当前上下文。
	if active := toolctx.SessionID(ctx); active != "" && record.SessionID != "" && record.SessionID != active {
		return artifactReadFailure(fmt.Sprintf(
			"artifact %s 属于会话 %s，不能在当前会话 %s 中读取",
			record.ID, record.SessionID, active,
		)), nil
	}

	content := record.Content
	total := len(content)

	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := p.Limit
	if limit <= 0 {
		limit = artifactReadDefaultLimitBytes
	}
	if maxLimit := artifactReadMaxLimitBytes(); limit > maxLimit {
		limit = maxLimit
	}

	start := artifactRuneStartAtOrBefore(content, offset)
	end := artifactRuneStartAtOrBefore(content, offset+limit)
	if end < start {
		end = start
	}
	// A tiny limit that lands mid-rune can snap back onto the window start and
	// stall the page chain; always advance at least one full rune.
	if end <= start && start < total {
		if _, size := utf8.DecodeRuneInString(content[start:]); size > 0 {
			end = start + size
		}
	}
	if end > total {
		end = total
	}
	window := content[start:end]
	eof := end >= total

	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"artifact %s | tool=%s | total_bytes=%d | window=[%d,%d) | eof=%t",
		record.ID, artifactReadToolLabel(record.ToolName), total, start, end, eof,
	)
	if !eof {
		fmt.Fprintf(&builder, " | next_offset=%d", end)
	}
	if record.ToolCallID != "" {
		builder.WriteString(" | tool_call_id=")
		builder.WriteString(record.ToolCallID)
	}
	builder.WriteString("\n\n")
	builder.WriteString(window)

	metadata := map[string]interface{}{
		"artifact_source_id":      record.ID,
		"artifact_tool_name":      record.ToolName,
		"artifact_total_bytes":    total,
		"artifact_window_offset":  start,
		"artifact_window_bytes":   len(window),
		"artifact_eof":            eof,
		"artifact_has_full_bytes": true,
	}
	if !eof {
		metadata["artifact_next_offset"] = end
	}
	if record.ToolCallID != "" {
		metadata["artifact_source_tool_call_id"] = record.ToolCallID
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    builder.String(),
		Metadata:   metadata,
	}, nil
}

// artifactReadMaxLimitBytes resolves the byte cap for a single window from the
// configurable model-visible tool text budget, so a full-window read stays under
// the truncation threshold (and never produces a new artifact pointer).
func artifactReadMaxLimitBytes() int {
	limit := output.ModelToolTextByteBudget() - artifactReadHeaderReserveBytes
	if limit < artifactReadMinLimitBytes {
		limit = artifactReadMinLimitBytes
	}
	return limit
}

// normalizeArtifactReadID accepts either a bare id or a pasted pointer tail and
// returns the art_<32hex> token when one is present.
func normalizeArtifactReadID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if match := artifactReadIDPattern.FindString(trimmed); match != "" {
		return strings.ToLower(match)
	}
	return strings.Trim(trimmed, "`\"'<>;,.")
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

func artifactReadToolLabel(toolName string) string {
	if label := strings.TrimSpace(toolName); label != "" {
		return label
	}
	return "unknown"
}

func artifactReadFailure(message string) *toolkit.ToolResult {
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      fmt.Errorf("%s", message),
	}
}
