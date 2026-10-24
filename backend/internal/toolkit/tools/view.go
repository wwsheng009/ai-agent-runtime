package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Batch defaults live in the shared handler; the aliases keep this surface
// reading the same numbers the handler applies.
const viewBatchDefaultLimit = viewcore.BatchDefaultLimit

const viewCompactHeadLines = viewcore.BatchCompactHeadLines

// Window bounds live in the shared reader. The default stays deliberately
// small for context economy: view owns its own 32 KiB byte budget and stamps
// skip_render_truncation, so callers page with offset/limit instead of
// pulling a whole file.
const viewDefaultLimit = viewcore.DefaultLimit

// viewByteBudgetBytes resolves the in-tool byte stop condition. view owns its
// own 32 KiB window (independent of the render-layer budget) and stamps
// skip_render_truncation on every result, so the window is never re-folded.
func viewByteBudgetBytes() int {
	return viewOutputBudgetBytes
}

// recordViewTruncation reports one truncation event from the shared reader to
// the local metrics sink. The reader owns the decision of *when* a window was
// cut; only the surface knows where to count it.
func recordViewTruncation(reason viewcore.TruncReason) {
	observability.RecordToolOutputTruncation(observability.TruncationLayerView, string(reason))
}

// viewReadOptions is the local surface's read configuration: view's own byte
// window plus the metric sink. Every read this surface performs - plain text,
// document render, notebook render - uses it, so one window semantics applies.
func viewReadOptions() viewcore.Options {
	return viewcore.Options{ByteBudget: viewByteBudgetBytes(), Truncated: recordViewTruncation}
}

// ViewTool 文件查看工具
type ViewTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxLineSize int64
	// symbolResolver 是 Phase 3 的 view --symbol 支持：nil 表示索引不可用
	// （symbol 参数退化为行范围读取/报错口径，04 §5 Phase 3 交付 4）。
	symbolResolver CodeIndexResolver
	// imageAttachmentTokenLimit bounds the estimated prompt cost of one
	// passthrough image. Zero disables the gate (legacy behaviour); callers
	// that know the prompt budget set it explicitly.
	imageAttachmentTokenLimit int
}

// NewViewTool 创建 View 工具
func NewViewTool() *ViewTool {
	return newViewTool("view")
}

// SetImageAttachmentTokenLimit bounds the estimated prompt cost of a
// passthrough image. Images above the limit degrade to a text note with the
// path, so one screenshot cannot pin an unshrinkable payload in the prompt
// (2026-10-19 incident). A non-positive value disables the gate.
func (v *ViewTool) SetImageAttachmentTokenLimit(limit int) {
	if v == nil {
		return
	}
	v.imageAttachmentTokenLimit = limit
}

// NewReadTool 创建 `read` 兼容别名工具。
//
// 别名与 view 共享同一参数 schema 与同一执行实现，只是把模型/IDE 习惯名
// `read` 注册成可直接调用的工具，使模型以 `read` 发起调用时落到 view，
// 而不是得到 "tool not found: read"。模型可见工具面仍以 view 为规范名：
// agent.optimizeModelToolSurface 会折叠掉这个别名。
func NewReadTool() *ViewTool {
	return newViewTool("read")
}

func newViewTool(name string) *ViewTool {
	description := "查看一个或多个文件。用 files 批量读取独立文件或区间；单文件用 file_path。输出包含稳定行号和截断元数据。" +
		"已知符号名但不知道行号时可传 symbol（索引可用时按符号范围读取；索引不可用时退化为 file_path 行范围或提示改用 grep）。"
	if name == "read" {
		description = "`read` 是 view 的兼容别名（同一 schema、同一执行实现）；新调用请优先使用 view。"
	}
	// One schema for every surface (viewcore.Schema). The local surface can
	// honor `symbol` through its code index; surfaces without an index filter
	// it out with toolcore.FilterSchemaParams instead of redeclaring a weaker
	// schema of their own.
	parameters := viewcore.Schema()

	return &ViewTool{
		BaseTool: toolkit.NewBaseTool(
			name,
			description,
			"1.1.0",
			parameters,
			true,
		),
		maxLineSize: 5 * 1024 * 1024,
	}
}

func (v *ViewTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
		// view targets local read inputs: the existence preflight keeps applying
		// (P1-4 path roles, declared explicitly so later changes cannot silently
		// reclassify them).
		runtimetypes.ToolMetadataPathRolesKey: map[string]interface{}{
			"file_path": runtimetypes.ToolPathRoleInput,
			"paths":     runtimetypes.ToolPathRoleInput,
			"files":     runtimetypes.ToolPathRoleInput,
		},
	}
}

type ViewParams struct {
	FilePath string            `json:"file_path,omitempty"`
	Files    []ViewFileRequest `json:"files,omitempty"`
	Offset   int               `json:"offset,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	Compact  bool              `json:"compact,omitempty"`
	Symbol   string            `json:"symbol,omitempty"`
}

// ViewFileRequest is one requested window: the top-level file_path and every
// files[] entry share the shape. The read state a request owes the host is no
// longer carried on the request: the shared handler owns the ordering (commit
// only after a section is really delivered), so the per-request defer closures
// this struct used to hold are gone.
type ViewFileRequest struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// coerceViewNumericParams converts string values for known integer fields
// (limit, offset) in the params map to actual integers, so that the strict
// json.Unmarshal into ViewParams does not reject them as "view 参数格式无效".
// This mirrors the coercion already present for code tools (codeParamInt).
// Only top-level and per-item files[] numeric fields are coerced; invalid
// numeric strings are left unchanged (they will still fail JSON decoding
// with the existing error, preserving the current behavior for garbage input).
func coerceViewNumericParams(params map[string]interface{}) {
	if params == nil {
		return
	}
	coerceIntField(params, "limit")
	coerceIntField(params, "offset")
	if files, ok := params["files"].([]interface{}); ok {
		for _, item := range files {
			if m, ok := item.(map[string]interface{}); ok {
				coerceIntField(m, "limit")
				coerceIntField(m, "offset")
			}
		}
	}
}

// coerceIntField converts a string value at params[key] to an int when it
// parses as a clean integer. Non-string values and non-integer strings are
// left untouched.
func coerceIntField(params map[string]interface{}, key string) {
	raw, ok := params[key]
	if !ok {
		return
	}
	str, ok := raw.(string)
	if !ok {
		return
	}
	trimmed := strings.TrimSpace(str)
	if trimmed == "" {
		return
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return
	}
	params[key] = parsed
}

// CoreRuntime exposes the local view tool's host capabilities as a
// toolcore.Runtime, so the shared view handler can run locally with the same
// permissions, the same read-before-write ledger, the same delivered-window
// cache, the same code index and the same document/notebook/image renderers the
// local tool has always had.
func (v *ViewTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if v == nil {
		return nil
	}
	runtime := newLocalCoreRuntimeWith(ctx, &v.sandboxPolicy, nil)
	if local, ok := runtime.(*localCoreRuntime); ok {
		// Capabilities, not behaviour: the shared handler asks for a window or a
		// rendered answer and never learns what produced it.
		local.symbolResolver = localSymbolResolver{tool: v, ctx: ctx}
		local.contentRenderer = localContentRenderer{tool: v, ctx: ctx}
	}
	return runtime
}

// Execute 实现 Tool 接口
//
// 本地面只剩两件事：把参数归一化（模型常把数字写成字符串），以及套上本地渲染层的
// 契约标记。读取、窗口、批处理、特殊路径拒绝、拼写治愈、符号解析与派生渲染的记账
// 全部走共享 handler，本地只注入宿主能力（沙箱、索引、渲染器、会话读状态）。
func (v *ViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 模型常把数字参数写成字符串（"104" 而非 104）：在 JSON 解码之前把已知的
	// 整数字段（limit/offset，包括 files[] 条目中的同名字段）从字符串形式
	// 强转为整数，避免 json.Unmarshal 因类型不匹配（string→int）直接拒绝为
	// "view 参数格式无效"。参见 code_common.go:codeParamInt 的同类处理。
	coerceViewNumericParams(params)
	var probe ViewParams
	encoded, err := json.Marshal(params)
	if err != nil || json.Unmarshal(encoded, &probe) != nil {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("view 参数格式无效"),
		}), nil
	}
	result, err := viewcore.NewHandler(v.coreDefinition()).Execute(ctx, &toolcore.Call{
		Args:    params,
		Runtime: v.CoreRuntime(ctx),
	})
	if err != nil {
		return nil, err
	}
	converted := viewCoreResult(result)
	// The declared window follows the answer, not the call shape: the shared
	// handler marks batch results, so the two paths cannot drift apart here.
	budget := viewOutputBudgetBytes
	if batch, _ := converted.Metadata["batch"].(bool); batch {
		budget = viewBatchAggregateBudgetBytes
	}
	return stampToolOwnsOutputWithBudget(converted, budget), nil
}

// coreDefinition is the shared handler's view of this tool: one schema, one
// declared window.
//
// The name is the canonical capability string, not the registered tool name:
// the read-before-write ledger keys on it, and a `read`-aliased call must be
// recognized as the same read the `view` call would have made.
func (v *ViewTool) coreDefinition() toolcore.Definition {
	return toolcore.Definition{
		Name:                "view",
		Version:             v.Version(),
		DefaultOutputBudget: viewOutputBudgetBytes,
	}
}

// SetCodeIndexResolver 注入 Phase 3 的只读索引解析器（view --symbol 支持）。
// nil 表示索引不可用：symbol 参数退化为行范围读取/报错口径。
func (v *ViewTool) SetCodeIndexResolver(resolver CodeIndexResolver) {
	if v == nil {
		return
	}
	v.symbolResolver = resolver
}

// resolveViewSymbol 解析 symbol 参数 → 按符号范围的行窗口请求。
// ok=false 表示索引不可用 / 符号不存在 / 路径未知（调用方走退化口径）。
func (v *ViewTool) resolveViewSymbol(ctx context.Context, symbol string, limit int) (ViewFileRequest, bool) {
	if v == nil || v.symbolResolver == nil {
		return ViewFileRequest{}, false
	}
	handle, ok := v.symbolResolver(ctx)
	if !ok || handle == nil || handle.Index == nil || strings.TrimSpace(handle.WorkspaceID) == "" {
		return ViewFileRequest{}, false
	}
	// shadow 档（04 §4.6 第 3 步）：索引候选不改变模型可见输出，symbol 参数
	// 退化为行范围读取/报错口径（与 code.* 的 shadow 语义一致）。
	if handle.Mode == knowledge.ModeShadow {
		return ViewFileRequest{}, false
	}
	sym, _, found, err := resolveCodeSymbol(ctx, handle.Index, symbol, "")
	if err != nil || !found {
		return ViewFileRequest{}, false
	}
	path := handle.PathForFile(sym.FileID)
	if path == "" {
		return ViewFileRequest{}, false
	}
	start := sym.Range.Start.Line - 1
	if start < 0 {
		start = 0
	}
	span := sym.Range.End.Line - sym.Range.Start.Line + 1
	if span <= 0 {
		span = 1
	}
	if limit > 0 && limit < span {
		span = limit
	}
	return ViewFileRequest{FilePath: path, Offset: start, Limit: span}, true
}

// viewCoreResult converts a result produced by the shared viewcore handler into
// the local shape. The shared code answers in surface-independent vocabulary
// (INVALID_ARGUMENT); the local surface keeps its own spelling so agents and
// diagnostics that classify on the string never see two vocabularies for one
// tool.
func viewCoreResult(result *toolcore.Result) *toolkit.ToolResult {
	return localizeErrorCode(toolkit.ResultToToolkit(result))
}

// renderContent answers the containers the shared text reader cannot read on its
// own: an image passthrough, a notebook, a document container. These renderers
// are the only view capabilities that stay host-side; the shared handler keeps
// the delivered-window bookkeeping, so a render that was never delivered is
// never remembered.
func (v *ViewTool) renderContent(ctx context.Context, req toolcore.RenderRequest) (*toolkit.ToolResult, bool) {
	info, err := os.Stat(req.AbsPath)
	if err != nil || info == nil {
		return nil, false
	}
	request := ViewFileRequest{FilePath: req.DisplayPath, Offset: req.Offset, Limit: req.Limit}
	// 图片直通：命中支持的图片时返回可发送的图片附件，而不是二进制报错。
	if imageResult, handled := v.viewImageResult(req.AbsPath, req.DisplayPath); handled {
		return imageResult, true
	}
	// notebook：渲染为带标签 Markdown 后复用同一窗口管线（analysis §3.10）。
	if notebookResult, handled := v.viewNotebookResult(req.AbsPath, req.DisplayPath, request, info); handled {
		return notebookResult, true
	}
	// 文档容器：渲染为 Markdown 后复用同一窗口；无转换器时降级为 MIME note
	// （analysis §3.9）。
	if documentResult, handled := v.viewDocumentResult(ctx, req.AbsPath, req.DisplayPath, request, info); handled {
		return documentResult, true
	}
	return nil, false
}

// viewMetadataInt reads an integer-like metadata value; derived renders build
// the numbers in-process, but a round trip through JSON would make them float64.
func viewMetadataInt(metadata map[string]interface{}, key string, fallback int) int {
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return fallback
}
