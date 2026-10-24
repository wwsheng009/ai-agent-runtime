package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// viewBatchDefaultLimit caps per-item reads in batch mode when the caller
// omits limit. Batch calls are for scanning many files, not pulling whole
// files, and a smaller default keeps the combined output inside the model
// context window instead of being truncated by the echo layer. Explicit
// per-item limits are always honored as-is.
const viewBatchDefaultLimit = 200

// viewCompactHeadLines is the per-file head window rendered in compact batch
// mode (scan-then-read workflow). Metadata still carries lines_read /
// is_truncated / suggested_next_offset / total_lines for follow-up reads.
const viewCompactHeadLines = 10

// viewDefaultLimit is the default window size when callers omit limit. It stays
// deliberately small for context economy; view owns its own 32 KiB byte budget
// and stamps skip_render_truncation, so callers that need more page with
// offset/limit instead of pulling a whole file.
const viewDefaultLimit = 400

// viewMaxLimit is the hard cap for an explicitly requested line window. Beyond
// it the model must page with offset: view publishes
// is_truncated/suggested_next_offset for exactly that continuation.
const viewMaxLimit = 2000

// viewEfficiencyAdvisoryThreshold marks when a default-size leading window is
// large enough that models should prefer narrower ranges or continue via offset.
const viewEfficiencyAdvisoryThreshold = 2000

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

type ViewFileRequest struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`

	// dedupDefer, when set, receives a dedup hit's consume closure instead of
	// running it immediately. The batch path uses it so an item dropped by the
	// aggregate cap does not burn the entry (review m13). Unexported: JSON
	// neither sets nor serializes it.
	dedupDefer func(viewDedupCommit)
	// dedupRecordDefer, when set, receives the dedup-registration closure instead
	// of running it immediately. The batch path defers it so an item dropped by
	// the aggregate cap does not leave a window in the "already delivered"
	// cache that the model never saw (2026-09-27 review).
	dedupRecordDefer func(func())
	// ledgerDefer, when set, receives the read-ledger closure instead of running
	// it immediately. The batch path defers it for the same reason as the dedup
	// registration: a cap-dropped section must not refresh the read-before-write
	// ledger, or a later write would treat the file as freshly read and
	// silently overwrite content the model never received (2026-09-27 review
	// H4).
	ledgerDefer func(func())
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

// CoreRuntime exposes the local view tool's sandbox and session read state as a
// toolcore.Runtime, so the shared view handler can run locally with the same
// permissions, the same read-before-write ledger and the same delivered-window
// cache the local tool has always kept.
func (v *ViewTool) CoreRuntime(ctx context.Context) toolcore.Runtime {
	if v == nil {
		return nil
	}
	return newLocalCoreRuntimeWith(ctx, &v.sandboxPolicy, nil)
}

// Execute 实现 Tool 接口
func (v *ViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 模型常把数字参数写成字符串（"104" 而非 104）：在 JSON 解码之前把已知的
	// 整数字段（limit/offset，包括 files[] 条目中的同名字段）从字符串形式
	// 强转为整数，避免 json.Unmarshal 因类型不匹配（string→int）直接拒绝为
	// "view 参数格式无效"。参见 code_common.go:codeParamInt 的同类处理。
	coerceViewNumericParams(params)
	var p ViewParams
	encoded, err := json.Marshal(params)
	if err != nil || json.Unmarshal(encoded, &p) != nil {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("view 参数格式无效"),
		}), nil
	}
	requests := make([]ViewFileRequest, 0, len(p.Files)+1)
	symbolNote := ""
	if symbol := strings.TrimSpace(p.Symbol); symbol != "" {
		if resolved, ok := v.resolveViewSymbol(ctx, symbol, p.Limit); ok {
			// symbol 优先：解析成功时忽略 file_path/offset/limit（按符号范围读取）。
			requests = append(requests, resolved)
			p.FilePath, p.Files, p.Offset, p.Limit = "", nil, 0, 0
		} else if strings.TrimSpace(p.FilePath) != "" {
			symbolNote = fmt.Sprintf("[note] symbol=%q 未能解析（索引不可用或符号不存在）；已按 file_path 的行范围读取。", symbol)
		} else {
			return stampToolOwnsOutput(&toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("view: symbol %q 无法解析（索引不可用或符号不存在）；请改用 file_path+offset/limit 或 grep", symbol),
			}), nil
		}
	}
	if strings.TrimSpace(p.FilePath) != "" {
		requests = append(requests, ViewFileRequest{FilePath: p.FilePath, Offset: p.Offset, Limit: p.Limit})
	}
	requests = append(requests, p.Files...)
	if len(requests) == 0 {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path、files 或 symbol 参数至少需要一个"),
		}), nil
	}
	// files 数组（即使只有 1 项）表达批量意图：应用批量默认 limit 与
	// compact 语义；仅 file_path 入参才是真正的单文件读取。
	var (
		result  *toolkit.ToolResult
		execErr error
	)
	if len(requests) == 1 && len(p.Files) == 0 {
		result, execErr = v.executeSingle(ctx, requests[0])
		if symbolNote != "" {
			result = annotateViewSymbolNote(result, symbolNote)
		}
		return stampToolOwnsOutputWithBudget(result, viewOutputBudgetBytes), execErr
	} else {
		result, execErr = v.executeBatch(ctx, requests, p.Compact)
		// A files[]-only call cannot honor top-level offset/limit (only
		// file_path does). Silently dropping them is the "silence still costs"
		// shape from analysis §3.1: state it instead so the model can move the
		// window onto the files[] entries.
		if len(p.Files) > 0 && strings.TrimSpace(p.FilePath) == "" && (p.Offset != 0 || p.Limit != 0) {
			result = annotateIgnoredBatchWindow(result, p.Offset, p.Limit)
		}
	}
	// 批量结果按聚合上限声明自己的可见窗口：声明 32 KiB 而实际交付到
	// 64 KiB 会让 gateway 把模型本可读全的正文错误归档（review m6）。
	return stampToolOwnsOutputWithBudget(result, viewBatchAggregateBudgetBytes), execErr
}

// annotateIgnoredBatchWindow tells the model that a batch call carried a
// top-level offset/limit that only applies to single-file reads.
func annotateIgnoredBatchWindow(result *toolkit.ToolResult, offset, limit int) *toolkit.ToolResult {
	if result == nil {
		return nil
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	note := fmt.Sprintf(
		"[note] 顶层 offset=%d limit=%d 未生效：files[] 批量读取需要在每个条目上设置 offset/limit。",
		offset, limit,
	)
	result.Metadata["ignored_top_level_window"] = map[string]interface{}{"offset": offset, "limit": limit}
	result.Metadata["ignored_top_level_window_note"] = note
	if strings.TrimSpace(result.Content) == "" {
		result.Content = note
	} else {
		result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + note
	}
	return result
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

// annotateViewSymbolNote 在 symbol 降级读取的结果上追加说明（不改变读取内容）。
func annotateViewSymbolNote(result *toolkit.ToolResult, note string) *toolkit.ToolResult {
	if result == nil || strings.TrimSpace(note) == "" {
		return result
	}
	if result.Metadata == nil {
		result.Metadata = map[string]interface{}{}
	}
	result.Metadata["view_symbol_note"] = note
	if strings.TrimSpace(result.Content) == "" {
		result.Content = note
	} else {
		result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + note
	}
	return result
}

func (v *ViewTool) executeSingle(ctx context.Context, p ViewFileRequest) (*toolkit.ToolResult, error) {
	if strings.TrimSpace(p.FilePath) == "" {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: fmt.Errorf("file_path 参数缺失或无效")}, nil
	}
	if p.Limit <= 0 {
		p.Limit = viewDefaultLimit
	}
	if p.Limit > viewMaxLimit {
		p.Limit = viewMaxLimit
	}
	// 设备/FIFO/Windows 保留名在任何 I/O 之前拒绝：open 一个 FIFO 会阻塞，
	// /dev/zero 会灌入无意义字节（analysis §3.2）。先用请求的原样拼写判一次，
	// 和 core handler 同序——否则同一次调用会因为「谁先跑」而得到两句话；
	// 解析后再判一次兜底。拒绝一律保持带 path_refused/refusal_reason 元数据的
	// 既有形状；checkPath 中的同一道门是给其余文件工具的兜底。
	if reason := unsupportedPathNameReason(p.FilePath); reason != "" {
		return viewCoreResult(viewcore.RefusalResult(p.FilePath, reason)), nil
	}
	resolvedPath := v.resolvePathWithContext(ctx, p.FilePath)
	if reason := unsupportedPathNameReason(resolvedPath); reason != "" {
		return viewCoreResult(viewcore.RefusalResult(resolvedPath, reason)), nil
	}
	// 检查文件是否存在
	if err := v.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	fileInfo, err := os.Stat(resolvedPath)
	pathHealed := false
	if err != nil && os.IsNotExist(err) {
		// 不可见文件名修复：磁盘名与请求名只差大小写/Unicode 拼写时，
		// 路径存在性检查会永远失败并且模型无法推理出正确字节（analysis §3.7）。
		if healed, candidates := v.healSpellingPath(ctx, resolvedPath); healed != "" {
			resolvedPath = healed
			pathHealed = true
			fileInfo, err = os.Stat(resolvedPath)
		} else if len(candidates) > 1 {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error: fmt.Errorf(
					"路径不存在: %s；父目录中存在多个仅大小写/Unicode 拼写不同的候选: %s。请确认后重试。",
					p.FilePath, strings.Join(candidates, ", "),
				),
				Metadata: map[string]interface{}{
					"file_path":       resolvedPath,
					"path_candidates": candidates,
					"path_auto_heal":  "ambiguous",
				},
			}, nil
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      v.buildPathNotFoundError(ctx, "路径不存在", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("无法访问文件: %w", err),
		}, nil
	}
	if reason := unsupportedFileModeReason(fileInfo.Mode()); reason != "" {
		return viewCoreResult(viewcore.RefusalResult(resolvedPath, reason)), nil
	}

	// 检查是否为目录：自动列出浅层内容，避免模型多一轮改用 ls。
	if fileInfo.IsDir() {
		listing, listErr := viewcore.DirectoryListing(resolvedPath, p.FilePath)
		if listErr != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("路径是目录，不是文件: %s；尝试列出内容失败: %w", p.FilePath, listErr),
			}, nil
		}
		return viewCoreResult(listing), nil
	}

	// 读取 limit 行（字节感知：累计输出超过模型可见预算的预留比例时提前停止，
	// 避免窗口被回显层 head/tail 折叠、把续读路径架空）
	// 图片直通：命中支持的图片时返回可发送的图片附件，而不是二进制报错。
	if imageResult, handled := v.viewImageResult(resolvedPath, p.FilePath); handled {
		return imageResult, nil
	}
	// notebook：渲染为带标签 Markdown 后复用同一窗口管线（analysis §3.10）。
	if notebookResult, handled := v.viewNotebookResult(resolvedPath, p.FilePath, p, fileInfo); handled {
		return v.recordDerivedRender(ctx, resolvedPath, notebookResult, p, fileInfo), nil
	}
	// 文档容器：渲染为 Markdown 后复用同一窗口；无转换器时降级为 MIME note
	// （analysis §3.9）。
	if documentResult, handled := v.viewDocumentResult(ctx, resolvedPath, p.FilePath, p, fileInfo); handled {
		return v.recordDerivedRender(ctx, resolvedPath, documentResult, p, fileInfo), nil
	}
	// Unchanged-window dedup: an exact repeat of a complete, unchanged window
	// returns a stub instead of paying for the same content twice
	// (analysis §3.6). The entry is consumed only once the stub is really
	// delivered: a batch item dropped by the aggregate cap must not burn it.
	if stub, hit, commit := viewDedupPeek(ctx, resolvedPath, fileInfo, p.Offset, p.Limit); hit {
		if p.dedupDefer != nil {
			p.dedupDefer(commit)
		} else {
			commit()
		}
		return stub, nil
	}
	content, readMeta, err := viewcore.ReadText(resolvedPath, viewcore.Request{
		FilePath: p.FilePath,
		Offset:   p.Offset,
		Limit:    p.Limit,
	}, viewReadOptions())
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      v.buildPathNotFoundError(ctx, "读取文件失败", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}

	// 检查是否为二进制文件
	if viewcore.IsBinaryContent(content) {
		// 二进制不再是一句无法行动的报错：给一行 MIME + 大小 + 恢复路径
		// （analysis §3.9：模型据此判断下一步是转码、下载还是换格式）。
		return viewCoreResult(viewcore.BinaryNote(resolvedPath, p.FilePath, fileInfo.Size())), nil
	}

	windowOffset := p.Offset
	windowLimit := p.Limit
	if readMeta.Tail {
		// Tail reads report the resolved absolute window, not the negative
		// request: continuation metadata must be usable as-is.
		windowOffset = readMeta.WindowStart
		windowLimit = readMeta.LinesRead
	}
	result := toolkit.ResultToToolkit(viewcore.BuildResult(resolvedPath, fileInfo.Size(), viewcore.Request{
		FilePath: p.FilePath,
		Offset:   p.Offset,
		Limit:    p.Limit,
	}, content, readMeta))
	if pathHealed {
		result.Metadata["path_auto_healed"] = true
		result.Metadata["original_path"] = p.FilePath
		result.Metadata["resolved_path"] = resolvedPath
		note := fmt.Sprintf(
			"[note] 路径 %q 按磁盘实际拼写解析为 %q（大小写/Unicode 差异）。后续调用请使用解析后的路径。",
			p.FilePath, resolvedPath,
		)
		result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + note
	}
	// 读账本：记录本次磁盘状态，供 write/edit 判断「读后是否被外部修改」。
	fullRead := p.Offset == 0 && !readMeta.HasMore && !readMeta.ByteBudgetApplied && readMeta.LongLinesTruncated == 0
	totalLines := 0
	if readMeta.TotalLinesKnown {
		totalLines = readMeta.TotalLines
	}
	recordLedger := func() {
		recordFileReadFromDiskObserved(ctx, resolvedPath, fullRead, "view", fileReadWindow{
			Offset:     windowOffset,
			Limit:      windowLimit,
			LinesRead:  readMeta.LinesRead,
			TotalLines: totalLines,
			Truncated:  readMeta.HasMore || readMeta.ByteBudgetApplied || readMeta.LongLinesTruncated > 0 || readMeta.Tail,
		}, fileInfo)
	}
	if p.ledgerDefer != nil {
		p.ledgerDefer(recordLedger)
	} else {
		recordLedger()
	}
	// Dedup only remembers complete text windows: a byte-budget stop or a line
	// clamp means a stub would hide content the model never saw.
	if !readMeta.Tail && readMeta.LinesRead > 0 && !readMeta.ByteBudgetApplied && readMeta.LongLinesTruncated == 0 && !readMeta.EmptyFile {
		recordWindow := func() {
			recordViewWindowRead(ctx, resolvedPath, fileInfo, p.Offset, p.Limit, readMeta.LinesRead, totalLines, readMeta.EOF)
		}
		// A batch registers the window only after the section is really
		// delivered: registering here would let a cap-dropped item stub a later
		// single read with content the model never received.
		if p.dedupRecordDefer != nil {
			p.dedupRecordDefer(recordWindow)
		} else {
			recordWindow()
		}
	}
	if readMeta.OriginalBytes > 0 {
		observability.RecordToolOutputBytes(observability.MetricToolOutputOriginalBytes, observability.TruncationLayerView, readMeta.OriginalBytes)
	}
	return result, nil
}

// viewCoreResult converts a result produced by the shared viewcore handler into
// the local shape. The shared code answers in surface-independent vocabulary
// (INVALID_ARGUMENT); the local surface keeps its own spelling so agents and
// diagnostics that classify on the string never see two vocabularies for one
// tool.
func viewCoreResult(result *toolcore.Result) *toolkit.ToolResult {
	return localizeErrorCode(toolkit.ResultToToolkit(result))
}

// healSpellingPath looks for directory entries whose name differs from the
// request only in case or invisible Unicode code points. A unique candidate is
// returned for the caller to use; multiple candidates are returned as a list
// for a "pick one" error. Every candidate is re-checked against the sandbox
// boundary: a repair must never become an escape hatch (analysis §3.7).
func (v *ViewTool) healSpellingPath(ctx context.Context, resolvedPath string) (string, []string) {
	dir, base := filepath.Dir(resolvedPath), filepath.Base(resolvedPath)
	matches := findSpellingMatches(dir, base)
	if len(matches) == 0 {
		return "", nil
	}
	candidates := make([]string, 0, len(matches))
	for _, name := range matches {
		candidate := filepath.Join(dir, name)
		if err := v.checkPath(runtimeexecutor.OpRead, candidate); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 1 {
		return candidates[0], candidates
	}
	return "", candidates
}

// recordDerivedRender closes the read-side bookkeeping for renders that do not
// travel through the text window path (notebook/document). Both renderers
// publish the same window metadata the text path uses, so the ledger and the
// dedup cache can be fed from it — without this, a write after viewing a
// document found no record and from-line-1 re-reads were never stubbed
// (review F11).
func (v *ViewTool) recordDerivedRender(ctx context.Context, resolvedPath string, result *toolkit.ToolResult, p ViewFileRequest, info os.FileInfo) *toolkit.ToolResult {
	if result == nil || !result.Success || info == nil || result.Metadata == nil {
		return result
	}
	metadata := result.Metadata
	// Same contract as the text path: consume only once the stub is delivered
	// (a batch item dropped by the aggregate cap must keep its entry).
	if stub, hit, commit := viewDedupPeek(ctx, resolvedPath, info, p.Offset, p.Limit); hit {
		if p.dedupDefer != nil {
			p.dedupDefer(commit)
		} else {
			commit()
		}
		return stub
	}
	offset := viewMetadataInt(metadata, "offset", p.Offset)
	limit := viewMetadataInt(metadata, "limit", p.Limit)
	linesRead := viewMetadataInt(metadata, "lines_read", 0)
	totalLines := viewMetadataInt(metadata, "total_lines", 0)
	truncated, _ := metadata["is_truncated"].(bool)
	tail, _ := metadata["tail"].(bool)
	empty, _ := metadata["empty"].(bool)
	// A reader-clamped line is hidden content: a derived render that folded a
	// long line must not be remembered as a complete window, or the next read
	// returns a stub for material the model never saw (2026-09-27 review).
	longLines := viewMetadataInt(metadata, "long_lines_truncated", 0)
	fullRead := !tail && offset == 0 && !truncated && linesRead > 0 && !empty && longLines == 0
	recordLedger := func() {
		recordFileReadFromDiskObserved(ctx, resolvedPath, fullRead, "view", fileReadWindow{
			Offset:     offset,
			Limit:      limit,
			LinesRead:  linesRead,
			TotalLines: totalLines,
			Truncated:  truncated || tail || longLines > 0,
		}, info)
	}
	if p.ledgerDefer != nil {
		p.ledgerDefer(recordLedger)
	} else {
		recordLedger()
	}
	if !tail && linesRead > 0 && !truncated && !empty && longLines == 0 {
		recordWindow := func() {
			recordViewWindowRead(ctx, resolvedPath, info, offset, limit, linesRead, totalLines, true)
		}
		if p.dedupRecordDefer != nil {
			p.dedupRecordDefer(recordWindow)
		} else {
			recordWindow()
		}
	}
	return result
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

// executeBatch runs a request list inside the shared aggregate window. The
// window itself - per-item defaults, the section budget, the skip accounting,
// the trailing blocks and the batch metadata - lives in viewcore so remote and
// local answer a batch identically; this adapter supplies the per-item read,
// which is where the local surface's own renderers and session state live.
func (v *ViewTool) executeBatch(ctx context.Context, requests []ViewFileRequest, compact bool) (*toolkit.ToolResult, error) {
	coreRequests := make([]viewcore.Request, 0, len(requests))
	for _, request := range requests {
		coreRequests = append(coreRequests, viewcore.Request{
			FilePath: request.FilePath,
			Offset:   request.Offset,
			Limit:    request.Limit,
		})
	}
	result := viewcore.ReadBatch(coreRequests, compact, func(req viewcore.Request) (*toolcore.Result, *viewcore.DeliveredRead) {
		request := ViewFileRequest{FilePath: req.FilePath, Offset: req.Offset, Limit: req.Limit}
		// The read state is committed by the shared batch, and only once the
		// section it belongs to was really appended: a cap-dropped item must not
		// burn its dedup entry or refresh the read ledger (review m13/H4).
		var delivered viewcore.DeliveredRead
		request.dedupDefer = func(commit viewDedupCommit) { delivered.Defer(func() { commit() }) }
		request.dedupRecordDefer = delivered.Defer
		request.ledgerDefer = delivered.Defer

		local, err := v.executeSingle(ctx, request)
		if err != nil {
			return &toolcore.Result{Success: false, OutputKind: toolcore.KindText, Content: err.Error(), Err: err}, &delivered
		}
		return toolkit.ResultFromToolkit(local), &delivered
	})
	return toolkit.ResultToToolkit(result), nil
}
