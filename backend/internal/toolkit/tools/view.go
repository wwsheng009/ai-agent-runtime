package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
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
}

// NewViewTool 创建 View 工具
func NewViewTool() *ViewTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "单个文件路径。也可改用 files 在一次调用中读取多个文件。",
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
			"view",
			"查看一个或多个文件。用 files 批量读取独立文件或区间；单文件用 file_path。输出包含稳定行号和截断元数据。",
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
	}
}

type ViewParams struct {
	FilePath string            `json:"file_path,omitempty"`
	Files    []ViewFileRequest `json:"files,omitempty"`
	Offset   int               `json:"offset,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	Compact  bool              `json:"compact,omitempty"`
}

type ViewFileRequest struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// Execute 实现 Tool 接口
func (v *ViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
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
	if strings.TrimSpace(p.FilePath) != "" {
		requests = append(requests, ViewFileRequest{FilePath: p.FilePath, Offset: p.Offset, Limit: p.Limit})
	}
	requests = append(requests, p.Files...)
	if len(requests) == 0 {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 或 files 参数至少需要一个"),
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
	} else {
		result, execErr = v.executeBatch(ctx, requests, p.Compact)
	}
	return stampToolOwnsOutput(result), execErr
}

func (v *ViewTool) executeSingle(ctx context.Context, p ViewFileRequest) (*toolkit.ToolResult, error) {
	if strings.TrimSpace(p.FilePath) == "" {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: fmt.Errorf("file_path 参数缺失或无效")}, nil
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Limit <= 0 {
		p.Limit = viewDefaultLimit
	}
	if p.Limit > viewMaxLimit {
		p.Limit = viewMaxLimit
	}
	resolvedPath := v.resolvePathWithContext(ctx, p.FilePath)

	// 检查文件是否存在
	if err := v.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	fileInfo, err := os.Stat(resolvedPath)
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
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("文件似乎是二进制文件，不支持显示"),
		}, nil
	}

	result := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    content,
		Metadata: map[string]interface{}{
			"file_path":    resolvedPath,
			"file_size":    fileInfo.Size(),
			"lines_read":   readMeta.LinesRead,
			"offset":       p.Offset,
			"limit":        p.Limit,
			"eof":          readMeta.EOF,
			"is_truncated": readMeta.HasMore,
		},
	}
	if readMeta.ByteBudgetApplied {
		result.Metadata["byte_budget_applied"] = true
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

// attachViewEfficiencyHints stamps continuation metadata and a soft advisory when
// a large leading window is truncated. This is non-blocking guidance only.
func attachViewEfficiencyHints(result *toolkit.ToolResult, request ViewFileRequest, readMeta viewReadResult) {
	if result == nil || result.Metadata == nil {
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

func (v *ViewTool) executeBatch(ctx context.Context, requests []ViewFileRequest, compact bool) (*toolkit.ToolResult, error) {
	sections := make([]string, 0, len(requests))
	items := make([]map[string]interface{}, 0, len(requests))
	failures := make([]string, 0)
	failedItems := make([]map[string]interface{}, 0)
	succeeded := 0
	defaulted := make([]int, 0, len(requests))
	for index, request := range requests {
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
		result, err := v.executeSingle(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", request.FilePath, err))
			if row := toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, err.Error()); row != nil {
				failedItems = append(failedItems, row)
			}
			continue
		}
		if result == nil || !result.Success {
			message := "unknown error"
			if result != nil && result.Error != nil {
				message = result.Error.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", request.FilePath, message))
			if row := toolresult.FailedItemMap(toolresult.IntPtr(index), request.FilePath, request.FilePath, message); row != nil {
				failedItems = append(failedItems, row)
			}
			continue
		}
		succeeded++
		section := fmt.Sprintf("===== %s =====\n%s", request.FilePath, result.Content)
		if compact {
			section += "\n" + compactViewSummary(result.Metadata)
		}
		sections = append(sections, section)
		items = append(items, result.Metadata)
	}
	if len(failures) > 0 {
		sections = append(sections, "===== errors =====\n"+strings.Join(failures, "\n"))
	}
	if succeeded == 0 {
		meta := map[string]interface{}{
			"batch":                       true,
			"request_count":               len(requests),
			"failed_count":                len(failures),
			"batch_default_limit_applied": defaulted,
			"compact":                     compact,
		}
		if len(failedItems) > 0 {
			meta[toolresult.MetadataFailedItemsKey] = failedItems
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("批量读取失败: %s", strings.Join(failures, "; ")),
			Metadata:   meta,
		}, nil
	}
	meta := map[string]interface{}{
		"batch":                       true,
		"request_count":               len(requests),
		"succeeded_count":             succeeded,
		"failed_count":                len(failures),
		"partial_failure":             len(failures) > 0,
		"items":                       items,
		"batch_default_limit_applied": defaulted,
		"compact":                     compact,
	}
	if len(failedItems) > 0 {
		meta[toolresult.MetadataFailedItemsKey] = failedItems
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    strings.Join(sections, "\n\n"),
		Metadata:   meta,
	}, nil
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

// readFile 读取文件内容
func (v *ViewTool) readFile(filePath string, offset, limit int) (string, viewReadResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", viewReadResult{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), int(v.maxLineSize))
	var lines []string
	meta := viewReadResult{}

	// 跳过 offset 行
	skipped := 0
	for skipped < offset && scanner.Scan() {
		skipped++
		meta.TotalLines++
	}

	if err := scanner.Err(); err != nil {
		return "", meta, err
	}
	if skipped < offset {
		meta.EOF = true
		meta.TotalLinesKnown = true
		return fmt.Sprintf("Reached end of file: offset %d is beyond total lines %d.", offset, meta.TotalLines), meta, nil
	}

	// 读取 limit 行，同时做字节感知提前停止
	readCount := 0
	accumulated := 0
	byteBudget := viewByteBudgetBytes()
	for readCount < limit && scanner.Scan() {
		raw := scanner.Text()
		meta.TotalLines++
		meta.OriginalBytes += len(raw) + 1 // + newline

		// 行内诚实截断：标记隐藏余量并计入丢失字节，而不是静默丢弃。
		var line string
		if utf8.RuneCountInString(raw) > viewMaxLineChars {
			var hidden int
			line, hidden = truncateLongLine(raw)
			meta.LongLinesTruncated++
			meta.HiddenBytes += hidden
			observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByBytes)
		} else {
			line = raw
		}

		lineBytes := len(line) + 12 // rendered prefix "<lineNum>: " + newline
		if readCount > 0 && accumulated+lineBytes > byteBudget {
			meta.HasMore = true
			meta.ByteBudgetApplied = true
			observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByBytes)
			break
		}

		lines = append(lines, line)
		accumulated += lineBytes
		readCount++
	}
	meta.LinesRead = readCount

	if err := scanner.Err(); err != nil {
		return "", meta, err
	}

	if scanner.Scan() {
		meta.TotalLines++
		meta.HasMore = true
		observability.RecordToolOutputTruncation(observability.TruncationLayerView, observability.TruncatedByLines)
	}
	if err := scanner.Err(); err != nil {
		return "", meta, err
	}
	if !meta.HasMore {
		meta.TotalLinesKnown = true
		meta.EOF = true
	}

	if readCount == 0 {
		meta.EOF = true
		meta.TotalLinesKnown = true
		if offset == meta.TotalLines {
			return fmt.Sprintf("Reached end of file: offset %d equals total lines %d.", offset, meta.TotalLines), meta, nil
		}
		return fmt.Sprintf("Reached end of file: offset %d is beyond total lines %d.", offset, meta.TotalLines), meta, nil
	}

	return v.formatContent(lines, offset), meta, nil
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
