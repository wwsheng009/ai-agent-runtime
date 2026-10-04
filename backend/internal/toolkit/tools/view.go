package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const viewDirPreviewLimit = 40

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

// viewReaderLineCapBytes bounds how many bytes of a single line the reader
// buffers before switching to drain-and-count mode. 256 KiB is far above the
// 2000-rune clamp (at most 8 KiB even for 4-byte runes), so every over-cap
// line is still clamped with an honest marker while a 400 MB single-line file
// never forces the whole line into memory (analysis §3.3).
const viewReaderLineCapBytes = 256 * 1024

// viewReaderClampedMarker replaces the hidden remainder of a line whose byte
// length exceeds viewReaderLineCapBytes. Unlike viewLongLineMarker it reports
// bytes (the drain path does not decode the hidden part), which is still an
// honest, countable-loss notice.
const viewReaderClampedMarker = "…[line truncated: showing first %d chars of a %d-byte line]"

// viewByteBudgetBytes resolves the in-tool byte stop condition. view owns its
// own 32 KiB window (independent of the render-layer budget) and stamps
// skip_render_truncation on every result, so the window is never re-folded.
func viewByteBudgetBytes() int {
	return viewOutputBudgetBytes
}

// ViewTool 文件查看工具
type ViewTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxLineSize int64
	// symbolResolver 是 Phase 3 的 view --symbol 支持：nil 表示索引不可用
	// （symbol 参数退化为行范围读取/报错口径，04 §5 Phase 3 交付 4）。
	symbolResolver CodeIndexResolver
}

// NewViewTool 创建 View 工具
func NewViewTool() *ViewTool {
	return newViewTool("view")
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
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "单个文件路径。也可改用 files 在一次调用中读取多个文件。",
			},
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "可选：按符号名读取（Phase 3）。索引可用时自动定位符号所在文件与行范围；索引不可用时若同时给了 file_path 则按行范围读取并附 note，否则报错并提示改用 grep 或 file_path+offset/limit。",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"description": "批量文件读取请求；适合一次获取多个独立文件或不同区间，减少 LLM 往返。批内未显式指定 limit 的项默认最多读取 200 行，避免合并输出过大被回显截断。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径。"},
						"offset":    map[string]interface{}{"type": "integer", "description": "0-based 起始行，默认 0。"},
						"limit":     map[string]interface{}{"type": "integer", "description": "读取行数，默认 400。"},
					},
					"required":             []string{"file_path"},
					"additionalProperties": false,
				},
			},
			"compact": map[string]interface{}{
				"type":        "boolean",
				"description": "批量扫描模式（仅 files 数组生效）：每个文件只输出前 10 行 + 元数据摘要（lines_read/is_truncated/suggested_next_offset/total_lines），适合先扫描多个文件再定点细读。默认 false。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）。大文件请配合 limit 分段查看；不要反复全量 view 同一路径。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取行数，默认 400；结果会标记 eof 和 is_truncated，以及 suggested_next_offset，便于按需继续。大文件优先更小 limit。",
			},
		},
		"required": []string{},
	}

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
	resolvedPath := v.resolvePathWithContext(ctx, p.FilePath)

	// 设备/FIFO/Windows 保留名在任何 I/O 之前拒绝：open 一个 FIFO 会阻塞，
	// /dev/zero 会灌入无意义字节（analysis §3.2）。放在 checkPath 之前，让
	// view 的拒绝保持带 path_refused/refusal_reason 元数据的既有形状；
	// checkPath 中的同一道门是给其余文件工具的兜底。
	if reason := unsupportedPathNameReason(resolvedPath); reason != "" {
		return v.unsupportedPathResult(resolvedPath, reason), nil
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
		return v.unsupportedPathResult(resolvedPath, reason), nil
	}

	// 检查是否为目录：自动列出浅层内容，避免模型多一轮改用 ls。
	if fileInfo.IsDir() {
		listing, listErr := v.listDirectoryPreview(resolvedPath)
		if listErr != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("路径是目录，不是文件: %s；尝试列出内容失败: %w", p.FilePath, listErr),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content: fmt.Sprintf(
				"路径是目录，不是文件: %s\n已自动列出目录内容（depth=1，最多 %d 项）。如需递归请改用 ls 并设置 depth。\n\n%s",
				p.FilePath,
				viewDirPreviewLimit,
				listing,
			),
			Metadata: map[string]interface{}{
				"file_path":    resolvedPath,
				"is_directory": true,
				"auto_listed":  true,
				"depth":        1,
			},
		}, nil
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
	content, readMeta, err := v.readFile(resolvedPath, p.Offset, p.Limit)
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
	if v.isBinaryFile(content) {
		// 二进制不再是一句无法行动的报错：给一行 MIME + 大小 + 恢复路径
		// （analysis §3.9：模型据此判断下一步是转码、下载还是换格式）。
		return v.binaryNoteResult(resolvedPath, p.FilePath, fileInfo), nil
	}

	windowOffset := p.Offset
	windowLimit := p.Limit
	if readMeta.Tail {
		// Tail reads report the resolved absolute window, not the negative
		// request: continuation metadata must be usable as-is.
		windowOffset = readMeta.WindowStart
		windowLimit = readMeta.LinesRead
	}
	result := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata: map[string]interface{}{
			"file_path":    resolvedPath,
			"file_size":    fileInfo.Size(),
			"lines_read":   readMeta.LinesRead,
			"offset":       windowOffset,
			"limit":        windowLimit,
			"eof":          readMeta.EOF,
			"is_truncated": readMeta.HasMore,
		},
	}
	if readMeta.Tail {
		result.Metadata["tail"] = true
		result.Metadata["tail_lines"] = readMeta.LinesRead
	}
	if readMeta.EmptyFile {
		result.Metadata["empty"] = true
	}
	if readMeta.ReaderClampedLines > 0 {
		result.Metadata["reader_clamped_lines"] = readMeta.ReaderClampedLines
		result.Metadata["reader_clamped_bytes"] = readMeta.ReaderClampedBytes
	}
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
	if readMeta.ByteBudgetApplied {
		result.Metadata["byte_budget_applied"] = true
	}
	if readMeta.Encoding != "" && readMeta.Encoding != fileEncodingUTF8.String() {
		result.Metadata["encoding"] = readMeta.Encoding
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
	// §10.6 honest-truncation contract: surface in-line loss instead of a
	// silent "...", so the model knows what it did not see and can decide
	// whether a second targeted read is worth the cost.
	if readMeta.LongLinesTruncated > 0 {
		result.Metadata["long_lines_truncated"] = readMeta.LongLinesTruncated
		result.Metadata["hidden_bytes"] = readMeta.HiddenBytes
	}
	if readMeta.OriginalBytes > 0 {
		observability.RecordToolOutputBytes(observability.MetricToolOutputOriginalBytes, observability.TruncationLayerView, readMeta.OriginalBytes)
	}
	if readMeta.TotalLinesKnown {
		result.Metadata["total_lines"] = readMeta.TotalLines
	}
	attachViewEfficiencyHints(result, p, readMeta)
	return result, nil
}

// binaryNoteResult describes a non-renderable binary file without failing the
// call: one line of MIME + size + a recovery route.
func (v *ViewTool) binaryNoteResult(absPath, displayPath string, info os.FileInfo) *toolkit.ToolResult {
	var size int64
	if info != nil {
		size = info.Size()
	}
	mimeType := ""
	if head, err := readViewFileHead(absPath, 512); err == nil {
		mimeType = strings.TrimSpace(http.DetectContentType(head))
	}
	if extMIME := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(absPath)))); extMIME != "" &&
		(mimeType == "" || mimeType == "application/octet-stream") {
		mimeType = extMIME
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content: fmt.Sprintf(
			"二进制文件: %s\nMIME: %s，大小: %d 字节。\n未附加文本或图像内容；可用 download 拉取，或按 MIME 选择外部工具转换后重试 view。",
			displayPath, mimeType, size,
		),
		Metadata: map[string]interface{}{
			"file_path":   absPath,
			"is_binary":   true,
			"binary_note": true,
			"binary_mime": mimeType,
			"file_size":   size,
		},
	}
}

// unsupportedPathResult turns a special-path refusal (device name, FIFO,
// socket, reserved Windows device) into a structured failure with a recovery
// route instead of a bare error.
func (v *ViewTool) unsupportedPathResult(path, reason string) *toolkit.ToolResult {
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      fmt.Errorf("不支持的特殊文件（%s）: %s；请改用 ls/glob 选择普通文件后重试。", reason, path),
		Metadata: map[string]interface{}{
			"file_path":      path,
			"path_refused":   true,
			"refusal_reason": reason,
		},
	}
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

// attachViewEfficiencyHints stamps continuation metadata and a soft advisory when
// a large leading window is truncated. This is non-blocking guidance only.
func attachViewEfficiencyHints(result *toolkit.ToolResult, request ViewFileRequest, readMeta viewReadResult) {
	if result == nil || result.Metadata == nil {
		return
	}
	// offset_resolved publishes the window the reader actually opened; for a
	// tail read that is the resolved absolute line, not the negative request
	// (review m11).
	result.Metadata["offset_resolved"] = readMeta.WindowStart
	if readMeta.Tail {
		// Tail windows are already the cheapest way to read a file's end, but a
		// clamped/cropped tail still needs the "read earlier" route: otherwise
		// the model has no way back into the part it never saw (review m11).
		if readMeta.TailClamped || readMeta.TailDroppedByBudget > 0 || readMeta.TailDroppedByLimit > 0 {
			if readMeta.TailClamped {
				result.Metadata["tail_clamped"] = true
			}
			if readMeta.TailDroppedByBudget > 0 {
				result.Metadata["tail_lines_dropped_by_budget"] = readMeta.TailDroppedByBudget
			}
			if readMeta.TailDroppedByLimit > 0 {
				result.Metadata["tail_lines_dropped_by_limit"] = readMeta.TailDroppedByLimit
			}
			result.Metadata["tail_window_start"] = readMeta.WindowStart
			if readMeta.WindowStart > 0 {
				earlier := readMeta.WindowStart - viewDefaultLimit
				if earlier < 0 {
					earlier = 0
				}
				result.Metadata["suggested_earlier_offset"] = earlier
				advisory := fmt.Sprintf(
					"[efficiency] tail window starts at line %d (lines_read=%d); for earlier content use offset=%d limit<=%d.",
					readMeta.WindowStart+1, readMeta.LinesRead, earlier, viewDefaultLimit,
				)
				if strings.TrimSpace(result.Content) == "" {
					result.Content = advisory
				} else {
					result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + advisory
				}
			}
		}
		return
	}
	if readMeta.HasMore {
		nextOffset := request.Offset + readMeta.LinesRead
		if nextOffset < request.Offset {
			nextOffset = request.Offset
		}
		result.Metadata["suggested_next_offset"] = nextOffset
	}
	// Soft-warn when a leading window still truncated: default-size windows
	// (or byte-budget-stopped windows) need continuation guidance; explicit
	// small ranges are already efficient; EOF windows need no advisory.
	if !readMeta.HasMore || request.Offset != 0 {
		return
	}
	if request.Limit < viewDefaultLimit && !readMeta.ByteBudgetApplied {
		return
	}
	nextOffset, _ := result.Metadata["suggested_next_offset"].(int)
	result.Metadata["efficiency_advisory"] = "prefer_offset_limit"
	advisory := fmt.Sprintf(
		"[efficiency] File continues past this window (is_truncated=true, lines_read=%d). Prefer a smaller limit for large files, or continue with offset=%d limit<=%d. For multiple independent files, use view.files in one call instead of repeated full-file views.",
		readMeta.LinesRead,
		nextOffset,
		viewDefaultLimit,
	)
	if strings.TrimSpace(result.Content) == "" {
		result.Content = advisory
		return
	}
	result.Content = strings.TrimRight(result.Content, "\n") + "\n\n" + advisory
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

// viewBatchFailureDetailLimit bounds how many per-item failures a batch lists
// in full. Without it, 400 unreadable files print into the errors block and the
// failed_items contract, pushing the delivered payload past the aggregate
// budget the tool declared (2026-09-27 review).
const viewBatchFailureDetailLimit = 20

// appendBatchFailure records one failure line while the detail budget lasts and
// counts the rest as omitted.
func appendBatchFailure(failures *[]string, omitted *int, line string) {
	if failures == nil || omitted == nil {
		return
	}
	if len(*failures) < viewBatchFailureDetailLimit {
		*failures = append(*failures, line)
		return
	}
	*omitted++
}

// appendBatchFailedItem records one structured failed-item row under the same
// detail budget. A nil row is ignored and does not consume budget.
func appendBatchFailedItem(items *[]map[string]interface{}, omitted *int, row map[string]interface{}) {
	if items == nil || omitted == nil || row == nil {
		return
	}
	if len(*items) < viewBatchFailureDetailLimit {
		*items = append(*items, row)
		return
	}
	*omitted++
}

func (v *ViewTool) executeBatch(ctx context.Context, requests []ViewFileRequest, compact bool) (*toolkit.ToolResult, error) {
	sections := make([]string, 0, len(requests))
	items := make([]map[string]interface{}, 0, len(requests))
	failures := make([]string, 0)
	failedItems := make([]map[string]interface{}, 0)
	failuresOmitted := 0
	failedItemsOmitted := 0
	succeeded := 0
	defaulted := make([]int, 0, len(requests))
	// Aggregate window: per-item byte budgets do not bound the combined
	// payload, and skip_render_truncation removes the render-layer backstop.
	// Once the budget is reached the remaining requests are reported as
	// skipped instead of silently flooding the context (analysis §3.1).
	skippedFiles := make([]map[string]interface{}, 0)
	emittedBytes := 0
	for index, request := range requests {
		if len(sections) > 0 && emittedBytes >= viewBatchSectionBudgetBytes() {
			skippedFiles = append(skippedFiles, map[string]interface{}{
				"index":     index,
				"file_path": request.FilePath,
				"reason":    "aggregate_budget",
			})
			continue
		}
		if request.Limit <= 0 {
			request.Limit = viewBatchDefaultLimit
			defaulted = append(defaulted, index)
		}
		if request.Limit > viewMaxLimit {
			request.Limit = viewMaxLimit
		}
		if compact && request.Limit > viewCompactHeadLines {
			request.Limit = viewCompactHeadLines
		}
		// Dedup hits are consumed only after the section is really appended:
		// an item dropped by the cap below must keep its entry (review m13).
		var pendingDedup viewDedupCommit
		request.dedupDefer = func(commit viewDedupCommit) { pendingDedup = commit }
		var pendingRecord func()
		request.dedupRecordDefer = func(record func()) { pendingRecord = record }
		var pendingLedger func()
		request.ledgerDefer = func(record func()) { pendingLedger = record }
		result, err := v.executeSingle(ctx, request)
		if err != nil {
			appendBatchFailure(&failures, &failuresOmitted, fmt.Sprintf("%s: %v", request.FilePath, err))
			appendBatchFailedItem(&failedItems, &failedItemsOmitted,
				toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, err.Error()))
			continue
		}
		if result == nil || !result.Success {
			message := "unknown error"
			if result != nil && result.Error != nil {
				message = result.Error.Error()
			}
			appendBatchFailure(&failures, &failuresOmitted, fmt.Sprintf("%s: %s", request.FilePath, message))
			appendBatchFailedItem(&failedItems, &failedItemsOmitted,
				toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, message))
			continue
		}
		section := fmt.Sprintf("===== %s =====\n%s", request.FilePath, result.Content)
		if compact {
			section += "\n" + compactViewSummary(result.Metadata)
		}
		if len(sections) > 0 && emittedBytes+len(section) > viewBatchSectionBudgetBytes() {
			skippedFiles = append(skippedFiles, map[string]interface{}{
				"index":     index,
				"file_path": request.FilePath,
				"reason":    "aggregate_budget",
			})
			continue
		}
		sections = append(sections, section)
		emittedBytes += len(section)
		items = append(items, result.Metadata)
		// Count only sections that are actually delivered: a cap-dropped item
		// must not be reported as read (review m5).
		succeeded = len(items)
		if pendingLedger != nil {
			pendingLedger()
		}
		if pendingDedup != nil {
			pendingDedup()
		}
		if pendingRecord != nil {
			pendingRecord()
		}
	}
	// The trailing blocks share one reserved window: appending them after the
	// last per-section cap check let the body exceed its own declared budget,
	// and the render contract then cut content whose ledger/dedup entries were
	// already committed (2026-09-27 review H9).
	var tail strings.Builder
	if len(skippedFiles) > 0 {
		fmt.Fprintf(&tail,
			"===== batch window =====\n本次批量输出受聚合上限 %d 字节保护：已读取 %d/%d 个文件，跳过 %d 个。请对跳过项单独调用 view（可先用 compact=true 扫描）。",
			viewBatchAggregateBudgetBytes, succeeded, len(requests), len(skippedFiles),
		)
	}
	if len(failures) > 0 || failuresOmitted > 0 {
		// The errors block is part of the same aggregate window as the file
		// sections: letting every failure print in full made 400 unreadable
		// files deliver ~95 KiB against a 64 KiB declaration (2026-09-27 review).
		if tail.Len() > 0 {
			tail.WriteString("\n")
		}
		tail.WriteString("===== errors =====\n")
		tail.WriteString(strings.Join(failures, "\n"))
		if failuresOmitted > 0 {
			if len(failures) > 0 {
				tail.WriteString("\n")
			}
			fmt.Fprintf(&tail, "... 另有 %d 个失败未逐条列出（失败合计 %d/%d）",
				failuresOmitted, len(failures)+failuresOmitted, len(requests))
		}
	}
	if tail.Len() > 0 {
		tailText := trimBatchTail(tail.String(), viewBatchTailReserveBytes)
		sections = append(sections, tailText)
		emittedBytes += len(tailText)
	}
	if succeeded == 0 {
		meta := map[string]interface{}{
			"batch":         true,
			"request_count": len(requests),
			// Total failures, not just the bounded detail rows: consumers
			// derive success counts from requested-failed, so under-reporting
			// failed_count invented successes for an all-failed batch
			// (2026-09-27 review H8).
			"failed_count":                len(failures) + failuresOmitted,
			"batch_default_limit_applied": defaulted,
			"compact":                     compact,
		}
		if len(failedItems) > 0 {
			meta[toolresult.MetadataFailedItemsKey] = failedItems
		}
		if failuresOmitted > 0 {
			meta["failures_omitted"] = failuresOmitted
			meta["failed_items_omitted"] = failedItemsOmitted
		}
		meta["batch_tail_reserve_bytes"] = viewBatchTailReserveBytes
		attachBatchWindowMetadata(meta, emittedBytes, skippedFiles)
		summary := strings.Join(failures, "; ")
		if failuresOmitted > 0 {
			if summary != "" {
				summary += "; "
			}
			summary += fmt.Sprintf("另有 %d 个失败未逐条列出", failuresOmitted)
		}
		if strings.TrimSpace(summary) == "" {
			summary = "没有可读取的条目"
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("批量读取失败: %s", summary),
			Metadata:   meta,
		}, nil
	}
	meta := map[string]interface{}{
		"batch":                       true,
		"request_count":               len(requests),
		"succeeded_count":             succeeded,
		"failed_count":                len(failures) + failuresOmitted,
		"partial_failure":             len(failures) > 0,
		"items":                       items,
		"batch_default_limit_applied": defaulted,
		"compact":                     compact,
	}
	if len(failedItems) > 0 {
		meta[toolresult.MetadataFailedItemsKey] = failedItems
	}
	if failuresOmitted > 0 {
		meta["failures_omitted"] = failuresOmitted
		meta["failed_items_omitted"] = failedItemsOmitted
	}
	meta["batch_tail_reserve_bytes"] = viewBatchTailReserveBytes
	attachBatchWindowMetadata(meta, emittedBytes, skippedFiles)
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    strings.Join(sections, "\n\n"),
		Metadata:   meta,
	}, nil
}

// attachBatchWindowMetadata publishes the aggregate-window accounting so the
// model (and tests) can tell how many bytes were emitted and which files were
// skipped to stay inside the cap.
func attachBatchWindowMetadata(meta map[string]interface{}, emittedBytes int, skipped []map[string]interface{}) {
	if meta == nil || len(skipped) == 0 {
		return
	}
	meta["batch_aggregate_budget_bytes"] = viewBatchAggregateBudgetBytes
	meta["batch_bytes_emitted"] = emittedBytes
	meta["batch_skipped_count"] = len(skipped)
	meta["batch_skipped_files"] = skipped
}

// viewBatchSectionBudgetBytes is the aggregate budget available to file
// sections: the declared window minus the reserve for the trailing blocks (and
// the render-layer contract header). Sections decide admission against this,
// so the assembled body — tail included — stays inside the declared window.
func viewBatchSectionBudgetBytes() int {
	budget := viewBatchAggregateBudgetBytes - viewBatchTailReserveBytes
	if budget < viewOutputBudgetBytes {
		budget = viewOutputBudgetBytes
	}
	return budget
}

// trimBatchTail keeps the trailing batch blocks inside their reserved window,
// cutting on a UTF-8 boundary and stating that the details were trimmed so the
// model is never silently missing the skipped-file/error accounting.
func trimBatchTail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	const marker = "\n... 尾部细节已按聚合预算截断（跳过项与失败数仍见 metadata）。"
	cut := limit - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimRight(text[:cut], "\n") + marker
}

// compactViewSummary renders a single-line metadata digest for compact batch
// mode, so models can decide the next offset without re-reading the tool
// schema or guessing from truncated content.
func compactViewSummary(metadata map[string]interface{}) string {
	parts := make([]string, 0, 4)
	if v, ok := metadata["lines_read"].(int); ok {
		parts = append(parts, fmt.Sprintf("lines_read=%d", v))
	}
	if v, ok := metadata["is_truncated"].(bool); ok {
		parts = append(parts, fmt.Sprintf("is_truncated=%t", v))
	}
	if v, ok := metadata["suggested_next_offset"].(int); ok {
		parts = append(parts, fmt.Sprintf("suggested_next_offset=%d", v))
	}
	if v, ok := metadata["total_lines"].(int); ok {
		parts = append(parts, fmt.Sprintf("total_lines=%d", v))
	}
	if len(parts) == 0 {
		return "..."
	}
	return "... " + strings.Join(parts, ", ")
}

type viewReadResult struct {
	TotalLines      int
	TotalLinesKnown bool
	LinesRead       int
	HasMore         bool
	EOF             bool
	// ByteBudgetApplied marks windows stopped by the byte budget rather than
	// the line limit, so callers can distinguish the truncation cause.
	ByteBudgetApplied bool
	// LongLinesTruncated counts lines cut by the in-line guard so the honest
	// truncation contract (plan §10.6) can surface how much was hidden.
	LongLinesTruncated int
	// HiddenBytes is the byte size of the hidden remainder of over-limit
	// in-line-truncated lines within this window.
	HiddenBytes int
	// OriginalBytes is the raw window byte size before the in-line guard.
	OriginalBytes int
	// Encoding is the detected on-disk text encoding (utf-8 / utf-8-bom /
	// utf-16le / utf-16be) used for this read.
	Encoding string
	// Tail marks a read requested with a negative offset (last N lines). When
	// set, WindowStart holds the resolved absolute first line and the reader
	// function already consumed the whole stream.
	Tail bool
	// TailRequested is the requested tail length before clamping.
	TailRequested int
	// TailClamped marks a tail request clamped to viewMaxLimit.
	TailClamped bool
	// TailDroppedByBudget / TailDroppedByLimit count kept lines the byte budget
	// or the explicit limit removed from the front of the tail window
	// (review m11).
	TailDroppedByBudget int
	TailDroppedByLimit  int
	// WindowStart is the resolved 0-based first line of the returned window
	// (equal to the requested offset for normal reads).
	WindowStart int
	// ReaderClampedLines counts lines whose byte length exceeded
	// viewReaderLineCapBytes and were drained (counted, not buffered).
	ReaderClampedLines int
	// ReaderClampedBytes is the hidden byte total of those lines.
	ReaderClampedBytes int64
	// EmptyFile marks a zero-line file so callers can publish an explicit note
	// instead of an offset-beyond-EOF message.
	EmptyFile bool
}

// viewMaxLineChars bounds a single rendered line (plan L1).
const viewMaxLineChars = 2000

// viewLongLineMarker replaces the hidden remainder of an over-limit line.
// It is honest by construction: the model sees that content was cut and how
// many characters remain hidden, instead of a bare silent "...".
const viewLongLineMarker = "…[line truncated: %d more chars]"

// truncateLongLine keeps a rune-safe prefix of an over-limit line and appends
// the honest marker reporting the hidden rune count. It returns the visible
// text and the hidden byte count for loss accounting.
func truncateLongLine(line string) (visible string, hiddenBytes int) {
	runes := []rune(line)
	if len(runes) <= viewMaxLineChars {
		return line, 0
	}
	marker := fmt.Sprintf(viewLongLineMarker, len(runes)-viewMaxLineChars)
	markerRunes := len([]rune(marker))
	prefixRunes := viewMaxLineChars - markerRunes
	if prefixRunes < 0 {
		prefixRunes = 0
	}
	visible = string(runes[:prefixRunes]) + marker
	return visible, len(line) - len(string(runes[:prefixRunes]))
}

// viewMaxDecodeBytes bounds in-memory decoding for BOM/UTF-16 files. Larger
// files keep the streaming path instead of risking a multi-megabyte allocation
// on every read.
const viewMaxDecodeBytes int64 = 8 << 20

// readFile 读取文件内容：UTF-8 走流式扫描；带 BOM/UTF-16 的文件先解码，
// 保证模型看到的是可读文本，同时把编码记入元数据。
func (v *ViewTool) readFile(filePath string, offset, limit int) (string, viewReadResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", viewReadResult{}, err
	}
	defer file.Close()

	if enc, hasBOM := probeFileEncoding(file); hasBOM {
		if info, statErr := file.Stat(); statErr == nil && info.Size() > viewMaxDecodeBytes {
			return "", viewReadResult{}, fmt.Errorf(
				"文件使用 %s 编码且超过 %d 字节，view 未做整文件解码；请改用 shell/download 转换后查看",
				enc, viewMaxDecodeBytes,
			)
		}
		data, readErr := io.ReadAll(file)
		if readErr != nil {
			return "", viewReadResult{}, readErr
		}
		text, actual := decodeFileBytes(data)
		content, meta, readErr := v.readLines(strings.NewReader(text), offset, limit)
		meta.Encoding = actual.String()
		return content, meta, readErr
	}

	return v.readLines(file, offset, limit)
}

// probeFileEncoding peeks the leading BOM and rewinds the reader.
func probeFileEncoding(file *os.File) (fileEncoding, bool) {
	var head [4]byte
	n, _ := file.Read(head[:])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fileEncodingUTF8, false
	}
	enc, _ := detectFileEncoding(head[:n])
	return enc, enc.hasBOM()
}

func (v *ViewTool) readLines(reader io.Reader, offset, limit int) (string, viewReadResult, error) {
	br := bufio.NewReaderSize(reader, 64*1024)
	meta := viewReadResult{}
	if offset < 0 {
		return v.readTailLines(br, -offset, limit, meta)
	}

	// 跳过 offset 行
	skipped := 0
	for skipped < offset {
		line, err := readViewLine(br)
		if err != nil {
			return "", meta, err
		}
		if !line.HasBytes {
			meta.EOF = true
			meta.TotalLinesKnown = true
			break
		}
		skipped++
		meta.TotalLines++
	}

	// 读取 limit 行，同时做字节感知提前停止
	var lines []string
	readCount := 0
	accumulated := 0
	byteBudget := viewByteBudgetBytes()
	for readCount < limit {
		line, err := readViewLine(br)
		if err != nil {
			return "", meta, err
		}
		if !line.HasBytes {
			break
		}
		meta.TotalLines++
		meta.OriginalBytes += int(line.ContentBytes) + 1 // + newline
		accountViewLineTruncation(&meta, line)

		lineBytes := len(line.Text) + 12 // rendered prefix "<lineNum>: " + newline
		if readCount > 0 && accumulated+lineBytes > byteBudget {
			meta.HasMore = true
			meta.ByteBudgetApplied = true
			observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByBytes)
			break
		}

		lines = append(lines, line.Text)
		accumulated += lineBytes
		readCount++
	}
	meta.LinesRead = readCount

	// Peek instead of consuming: a huge next line must not be drained just to
	// learn that it exists.
	if !meta.HasMore && readCount == limit {
		if _, err := br.Peek(1); err == nil {
			meta.HasMore = true
			observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByLines)
		} else if err != io.EOF {
			return "", meta, err
		}
	}
	if !meta.HasMore {
		meta.TotalLinesKnown = true
		meta.EOF = true
	}

	if readCount == 0 {
		meta.EOF = true
		meta.TotalLinesKnown = true
		meta.WindowStart = offset
		if meta.TotalLines == 0 {
			meta.EmptyFile = true
			return "Note: file is empty (0 lines).", meta, nil
		}
		last := meta.TotalLines - 1
		if offset == meta.TotalLines {
			return fmt.Sprintf("Note: offset %d equals total lines %d; use offset %d to read the last line.", offset, meta.TotalLines, last), meta, nil
		}
		return fmt.Sprintf("Note: offset %d is beyond the end of the file (%d lines); retry with a smaller offset (0..%d).", offset, meta.TotalLines, last), meta, nil
	}

	meta.WindowStart = offset
	return v.formatContent(lines, offset), meta, nil
}

// readTailLines implements a negative offset: it streams the whole file while
// keeping only the last N lines (bounded memory), then trims the kept window
// from the front until it fits the byte budget - the tail must stay the true
// tail, so the oldest kept lines are the ones dropped.
func (v *ViewTool) readTailLines(br *bufio.Reader, requested, limit int, meta viewReadResult) (string, viewReadResult, error) {
	meta.TailRequested = requested
	tailLines := requested
	if tailLines > viewMaxLimit {
		tailLines = viewMaxLimit
		meta.TailClamped = true
	}
	if tailLines < 1 {
		// -MinInt 取负仍为负数（溢出），0/负值会让 make() panic；尾部读取
		// 至少保留一行。
		tailLines = 1
	}
	ring := make([]string, tailLines)
	count := 0
	for {
		line, err := readViewLine(br)
		if err != nil {
			return "", meta, err
		}
		if !line.HasBytes {
			break
		}
		count++
		meta.OriginalBytes += int(line.ContentBytes) + 1
		accountViewLineTruncation(&meta, line)
		// Slot for the 1-based line number count; orderedTailLines undoes this
		// rotation with count%capacity as the oldest-slot index.
		ring[(count-1)%tailLines] = line.Text
	}
	meta.TotalLines = count
	meta.TotalLinesKnown = true
	meta.EOF = true
	meta.Tail = true
	ordered := orderedTailLines(ring, count)
	kept := ordered
	if len(ordered) > 0 {
		kept = trimTailToByteBudget(ordered, viewByteBudgetBytes())
		meta.TailDroppedByBudget = len(ordered) - len(kept)
	}
	// An explicit limit also bounds a tail window: the newest lines are the
	// recovery-relevant ones, so the oldest kept lines are dropped (review m11).
	if limit > 0 && len(kept) > limit {
		meta.TailDroppedByLimit = len(kept) - limit
		kept = kept[len(kept)-limit:]
	}
	meta.WindowStart = count - len(kept)
	meta.LinesRead = len(kept)
	if len(kept) == 0 {
		meta.EmptyFile = true
		return "Note: file is empty (0 lines).", meta, nil
	}
	return v.formatContent(kept, meta.WindowStart), meta, nil
}

// orderedTailLines returns the ring contents oldest-first. When the file has
// fewer lines than the ring capacity the prefix slice is already in order.
func orderedTailLines(ring []string, count int) []string {
	if count == 0 {
		return nil
	}
	capacity := len(ring)
	if count <= capacity {
		return ring[:count]
	}
	start := count % capacity
	ordered := make([]string, 0, capacity)
	ordered = append(ordered, ring[start:]...)
	ordered = append(ordered, ring[:start]...)
	return ordered
}

// trimTailToByteBudget drops the oldest lines until the remaining window fits
// the budget. The newest line is always kept so the result still carries the
// recovery-relevant end of the file.
func trimTailToByteBudget(lines []string, budget int) []string {
	start := len(lines)
	accumulated := 0
	for index := len(lines) - 1; index >= 0; index-- {
		lineBytes := len(lines[index]) + 12
		if start < len(lines) && accumulated+lineBytes > budget {
			break
		}
		accumulated += lineBytes
		start = index
	}
	return lines[start:]
}

// viewLineRead is one logical line produced by readViewLine. Text is already
// clamped to viewMaxLineChars; ContentBytes is the full on-disk line length
// (delimiter excluded) even when the hidden remainder was never buffered.
type viewLineRead struct {
	Text         string
	HasBytes     bool
	EOF          bool
	ContentBytes int64
	HiddenChars  int
	HiddenBytes  int64
	OverClamped  bool
}

// readViewLine reads one line while buffering at most viewReaderLineCapBytes.
// Past the cap the remainder is drained and counted but never held in memory,
// so a multi-hundred-megabyte single-line file cannot exhaust memory or turn
// into a fatal "token too long" error (analysis §3.3).
func readViewLine(br *bufio.Reader) (viewLineRead, error) {
	var (
		buf       []byte
		over      bool
		total     int64
		lastChunk []byte
	)
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			total += int64(len(chunk))
			lastChunk = chunk
			if !over {
				if room := viewReaderLineCapBytes - len(buf); len(chunk) <= room {
					buf = append(buf, chunk...)
				} else {
					if room > 0 {
						buf = append(buf, chunk[:room]...)
					}
					over = true
				}
			}
		}
		switch err {
		case nil:
			return buildViewLine(buf, over, total, lastChunk, false), nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return buildViewLine(buf, over, total, lastChunk, true), nil
		default:
			return viewLineRead{}, err
		}
	}
}

// buildViewLine applies the rune clamp to a buffered line and reports honest
// hidden-byte accounting for both the buffered clamp and the reader drain.
func buildViewLine(buf []byte, over bool, total int64, lastChunk []byte, eof bool) viewLineRead {
	line := viewLineRead{HasBytes: total > 0, EOF: eof, OverClamped: over}
	delimiter := int64(0)
	if n := len(lastChunk); n > 0 && lastChunk[n-1] == '\n' {
		delimiter = 1
		if n > 1 && lastChunk[n-2] == '\r' {
			delimiter = 2
		}
	}
	line.ContentBytes = total - delimiter
	if line.ContentBytes < 0 {
		line.ContentBytes = 0
	}
	if !line.HasBytes {
		return line
	}
	text := string(buf)
	if !over && delimiter > 0 {
		text = strings.TrimSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\r")
	}
	if over {
		visible := clampLinePrefix(text, viewMaxLineChars)
		line.Text = visible + fmt.Sprintf(viewReaderClampedMarker, viewMaxLineChars, line.ContentBytes)
		hidden := line.ContentBytes - int64(len(visible))
		if hidden < 0 {
			hidden = 0
		}
		line.HiddenBytes = hidden
		return line
	}
	if utf8.RuneCountInString(text) > viewMaxLineChars {
		visible, hiddenBytes := truncateLongLine(text)
		line.Text = visible
		line.HiddenChars = utf8.RuneCountInString(text) - viewMaxLineChars
		line.HiddenBytes = int64(hiddenBytes)
		return line
	}
	line.Text = text
	return line
}

// accountViewLineTruncation folds one line's clamp/drain loss into the window
// metadata and the truncation metric.
func accountViewLineTruncation(meta *viewReadResult, line viewLineRead) {
	if line.OverClamped || line.HiddenChars > 0 {
		meta.LongLinesTruncated++
		meta.HiddenBytes += int(line.HiddenBytes)
		observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByBytes)
	}
	if line.OverClamped {
		meta.ReaderClampedLines++
		meta.ReaderClampedBytes += line.HiddenBytes
	}
}

// clampLinePrefix returns the prefix of s up to max runes without splitting a
// rune. It walks rune starts only, so a 256 KiB over-cap prefix costs no
// full-string rune conversion.
func clampLinePrefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for index := range s {
		if count == max {
			return s[:index]
		}
		count++
	}
	return s
}

// formatContent 格式化内容
func (v *ViewTool) formatContent(lines []string, offset int) string {
	if len(lines) == 0 {
		return ""
	}
	var output strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&output, "%d: %s", offset+index+1, line)
		if index < len(lines)-1 {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

// listDirectoryPreview returns a shallow directory listing for auto-heal when
// view is pointed at a directory instead of a file.
func (v *ViewTool) listDirectoryPreview(dirPath string) (string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	limit := viewDirPreviewLimit
	if len(names) < limit {
		limit = len(names)
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d: %s", i+1, names[i])
	}
	if len(names) > viewDirPreviewLimit {
		fmt.Fprintf(&b, "\n... 省略 %d 项（共 %d）", len(names)-viewDirPreviewLimit, len(names))
	}
	if len(names) == 0 {
		return "(空目录)", nil
	}
	return b.String(), nil
}

// isBinaryFile 检查文件是否为二进制文件
func (v *ViewTool) isBinaryFile(content string) bool {
	if len(content) == 0 {
		return false
	}

	// 检查前 500 个字节
	checkLen := 500
	if len(content) < checkLen {
		checkLen = len(content)
	}

	nullCount := 0
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			nullCount++
		}
	}

	// 如果超过一定比例是 null 字节，认为是二进制文件
	return nullCount > checkLen/20
}
