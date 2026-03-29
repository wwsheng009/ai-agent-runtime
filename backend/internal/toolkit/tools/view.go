package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"unicode/utf8"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
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
				"description": "要查看的文件路径",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "开始读取的行号（0-based，默认为 0）",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取的行数（默认为 2000）",
			},
		},
		"required": []string{"file_path"},
	}

	return &ViewTool{
		BaseTool: toolkit.NewBaseTool(
			"view",
			"查看文件内容",
			"1.0.0",
			parameters,
			true,
		),
		maxReadSize: 5 * 1024 * 1024, // 5MB
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
			Success: false,
			Error:   fmt.Errorf("file_path 参数缺失或无效"),
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
			Success: false,
			Error:   err,
		}, nil
	}
	fileInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法访问文件: %w", err),
		}, nil
	}

	// 检查是否为目录
	if fileInfo.IsDir() {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("路径是目录，不是文件: %s", p.FilePath),
		}, nil
	}

	// 检查文件大小
	if fileInfo.Size() > v.maxReadSize {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("文件过大（超过 %d MB），无法读取", v.maxReadSize/(1024*1024)),
		}, nil
	}

	// 读取文件
	content, err := v.readFile(resolvedPath, p.Offset, p.Limit)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}

	// 检查是否为二进制文件
	if v.isBinaryFile(content) {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("文件似乎是二进制文件，不支持显示"),
		}, nil
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: content,
		Metadata: map[string]interface{}{
			"file_path":    resolvedPath,
			"file_size":    fileInfo.Size(),
			"lines_read":   v.countLines(content),
			"is_truncated": p.Offset > 0 || p.Limit > 0,
		},
	}, nil
}

// readFile 读取文件内容
func (v *ViewTool) readFile(filePath string, offset, limit int) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	// 跳过 offset 行
	skipped := 0
	for skipped < offset && scanner.Scan() {
		skipped++
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	// 读取 limit 行
	var lines []string
	readCount := 0
	for readCount < limit && scanner.Scan() {
		line := scanner.Text()

		// 跳过过长的行
		if utf8.RuneCountInString(line) > 2000 {
			line = line[:2000] + "..."
		}

		lines = append(lines, line)
		readCount++
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	// 如果还有更多内容，添加提示
	if readCount == limit && scanner.Scan() {
		// 还有更多行
	}

	return v.formatContent(lines), nil
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

// countLines 统计行数
func (v *ViewTool) countLines(content string) int {
	if content == "" {
		return 0
	}

	count := 0
	for _, c := range content {
		if c == '\n' {
			count++
		}
	}
	return count + 1
}
