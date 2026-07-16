package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

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
				"description": "批量文件读取请求；适合一次获取多个独立文件或不同区间，减少 LLM 往返。",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string", "description": "文件路径。"},
						"offset":    map[string]interface{}{"type": "integer", "description": "0-based 起始行，默认 0。"},
						"limit":     map[string]interface{}{"type": "integer", "description": "读取行数，默认 2000。"},
					},
					"required":             []string{"file_path"},
					"additionalProperties": false,
				},
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）。大文件建议配合 limit 分段查看。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取行数，默认 2000；结果会标记 eof 和 is_truncated，便于按需继续。",
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
		runtimetypes.ToolMetadataSupportsParallelKey: true,
	}
}

type ViewParams struct {
	FilePath string            `json:"file_path,omitempty"`
	Files    []ViewFileRequest `json:"files,omitempty"`
	Offset   int               `json:"offset,omitempty"`
	Limit    int               `json:"limit,omitempty"`
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
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("view 参数格式无效"),
		}, nil
	}
	requests := make([]ViewFileRequest, 0, len(p.Files)+1)
	if strings.TrimSpace(p.FilePath) != "" {
		requests = append(requests, ViewFileRequest{FilePath: p.FilePath, Offset: p.Offset, Limit: p.Limit})
	}
	requests = append(requests, p.Files...)
	if len(requests) == 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 或 files 参数至少需要一个"),
		}, nil
	}
	if len(requests) == 1 {
		return v.executeSingle(ctx, requests[0])
	}
	return v.executeBatch(ctx, requests)
}

func (v *ViewTool) executeSingle(ctx context.Context, p ViewFileRequest) (*toolkit.ToolResult, error) {
	if strings.TrimSpace(p.FilePath) == "" {
		return &toolkit.ToolResult{Success: false, OutputKind: toolresult.KindText, Error: fmt.Errorf("file_path 参数缺失或无效")}, nil
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Limit <= 0 {
		p.Limit = 2000
	}
	resolvedPath := v.resolvePath(p.FilePath)

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
				Error:      v.buildPathNotFoundError("路径不存在", p.FilePath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("无法访问文件: %w", err),
		}, nil
	}

	// 检查是否为目录
	if fileInfo.IsDir() {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      v.buildPathKindMismatchError("路径是目录，不是文件", p.FilePath),
		}, nil
	}

	// 读取文件
	content, readMeta, err := v.readFile(resolvedPath, p.Offset, p.Limit)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      v.buildPathNotFoundError("读取文件失败", p.FilePath),
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
	if readMeta.TotalLinesKnown {
		result.Metadata["total_lines"] = readMeta.TotalLines
	}
	return result, nil
}

func (v *ViewTool) executeBatch(ctx context.Context, requests []ViewFileRequest) (*toolkit.ToolResult, error) {
	sections := make([]string, 0, len(requests))
	items := make([]map[string]interface{}, 0, len(requests))
	failures := make([]string, 0)
	succeeded := 0
	for _, request := range requests {
		result, err := v.executeSingle(ctx, request)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", request.FilePath, err))
			continue
		}
		if result == nil || !result.Success {
			message := "unknown error"
			if result != nil && result.Error != nil {
				message = result.Error.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", request.FilePath, message))
			continue
		}
		succeeded++
		sections = append(sections, fmt.Sprintf("===== %s =====\n%s", request.FilePath, result.Content))
		items = append(items, result.Metadata)
	}
	if len(failures) > 0 {
		sections = append(sections, "===== errors =====\n"+strings.Join(failures, "\n"))
	}
	if succeeded == 0 {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("批量读取失败: %s", strings.Join(failures, "; ")),
			Metadata: map[string]interface{}{
				"batch":         true,
				"request_count": len(requests),
				"failed_count":  len(failures),
			},
		}, nil
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    strings.Join(sections, "\n\n"),
		Metadata: map[string]interface{}{
			"batch":           true,
			"request_count":   len(requests),
			"succeeded_count": succeeded,
			"failed_count":    len(failures),
			"partial_failure": len(failures) > 0,
			"items":           items,
		},
	}, nil
}

type viewReadResult struct {
	TotalLines      int
	TotalLinesKnown bool
	LinesRead       int
	HasMore         bool
	EOF             bool
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

	// 读取 limit 行
	readCount := 0
	for readCount < limit && scanner.Scan() {
		line := scanner.Text()
		meta.TotalLines++

		// 跳过过长的行
		if utf8.RuneCountInString(line) > 2000 {
			line = string([]rune(line)[:2000]) + "..."
		}

		lines = append(lines, line)
		readCount++
	}
	meta.LinesRead = readCount

	if err := scanner.Err(); err != nil {
		return "", meta, err
	}

	if scanner.Scan() {
		meta.TotalLines++
		meta.HasMore = true
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
