package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
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
	maxReadSize int64
}

// NewViewTool 创建 View 工具
func NewViewTool() *ViewTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要查看的文件路径。若需要查看多个文件，请拆分为多次 view 调用，每次只聚焦一个文件。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）。大文件建议配合 limit 分段查看。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取的行数（默认为 2000）。若文件较大，建议保持较小的单次读取范围并分次查看。",
			},
		},
		"required": []string{"file_path"},
	}

	return &ViewTool{
		BaseTool: toolkit.NewBaseTool(
			"view",
			"查看文件内容。若需要查看多个文件，请拆分为多次 view 调用，每次只聚焦一个文件；大文件建议按 offset/limit 分段查看。",
			"1.0.0",
			parameters,
			true,
		),
		maxReadSize: 5 * 1024 * 1024, // 5MB
	}
}

func (v *ViewTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: true,
	}
}

type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// Execute 实现 Tool 接口
func (v *ViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p ViewParams

	// 解析参数
	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 参数缺失或无效"),
		}, nil
	}

	p.FilePath = filePath
	resolvedPath := v.resolvePath(p.FilePath)

	if offset, ok := params["offset"].(float64); ok {
		p.Offset = int(offset)
	}
	if offset, ok := params["offset"].(int64); ok {
		p.Offset = int(offset)
	}

	if limit, ok := params["limit"].(float64); ok {
		p.Limit = int(limit)
	}
	if limit, ok := params["limit"].(int64); ok {
		p.Limit = int(limit)
	}

	if p.Limit <= 0 {
		p.Limit = 2000 // 默认值
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

	// 检查文件大小
	if fileInfo.Size() > v.maxReadSize {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("文件过大（超过 %d MB），无法读取", v.maxReadSize/(1024*1024)),
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
			line = line[:2000] + "..."
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

	return v.formatContent(lines), meta, nil
}

// formatContent 格式化内容
func (v *ViewTool) formatContent(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%s", lines)
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
