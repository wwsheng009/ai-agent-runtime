package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// WriteTool 文件写入工具
type WriteTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// NewWriteTool 创建 Write 工具
func NewWriteTool() *WriteTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要写入的文件路径",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "文件内容",
			},
		},
		"required": []string{"file_path", "content"},
	}

	return &WriteTool{
		BaseTool: toolkit.NewBaseTool(
			"write",
			"创建或覆盖文件内容",
			"1.0.0",
			parameters,
			true,
		),
	}
}

type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// Execute 实现 Tool 接口
func (w *WriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p WriteParams

	// 解析参数
	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("file_path 参数缺失或无效"),
		}, nil
	}
	p.FilePath = filePath

	content, ok := params["content"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("content 参数缺失或无效"),
		}, nil
	}
	p.Content = content
	resolvedPath := w.resolvePath(p.FilePath)

	if err := w.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	// 转换为绝对路径
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("解析文件路径失败: %w", err),
		}, nil
	}

	// 检查文件是否已存在
	fileExists := false
	var oldSize int64 = 0
	oldContent := ""
	if fileInfo, err := os.Stat(absPath); err == nil {
		fileExists = true
		oldSize = fileInfo.Size()
		if !fileInfo.Mode().IsRegular() {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("路径已存在但不是常规文件: %s", absPath),
			}, nil
		}
		if content, readErr := os.ReadFile(absPath); readErr == nil {
			oldContent = string(content)
		}
	}

	// 创建父目录
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("创建目录失败: %w", err),
		}, nil
	}

	// 写入文件
	err = os.WriteFile(absPath, []byte(p.Content), 0644)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("写入文件失败: %w", err),
		}, nil
	}

	// 获取新文件信息
	newInfo, err := os.Stat(absPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("获取文件信息失败: %w", err),
		}, nil
	}

	action := "创建"
	if fileExists {
		action = "覆盖"
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: fmt.Sprintf("成功%s文件: %s\n文件大小: %d 字节", action, absPath, newInfo.Size()),
		Metadata: map[string]interface{}{
			"file_path":     absPath,
			"action":        action,
			"old_existed":   fileExists,
			"old_size":      oldSize,
			"new_size":      newInfo.Size(),
			"size_changed":  int64(len(p.Content)) - oldSize,
			"patch":         buildUnifiedPatch(absPath, oldContent, p.Content),
			"mutated_paths": []string{absPath},
		},
	}, nil
}
