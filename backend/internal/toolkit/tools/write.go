package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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
				"description": "要写入的文件路径。若需要写入多个文件，请拆分为多次 write 调用，每次只聚焦一个文件。",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "文件内容。若内容较长，请拆分为多个更小的写入块，按章节或按块逐步写入，避免一次性生成超长参数导致工具调用被截断。",
			},
		},
		"required": []string{"file_path", "content"},
	}

	return &WriteTool{
		BaseTool: toolkit.NewBaseTool(
			"write",
			"创建或覆盖单个文件内容；适合写入完整文件或较小分块。若需要写入多个文件或较长内容，请拆分为多个更小的 write/edit 调用，每次只聚焦一个文件和一个写入目标，按章节或按块逐步写入，避免单次工具参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

func (w *WriteTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: false,
	}
}

type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// Execute 实现 Tool 接口
func (w *WriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p WriteParams

	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}

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

	content, ok := params["content"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("content 参数缺失或无效"),
		}, nil
	}
	p.Content = content
	if err := validateInlineFileMutationPayload("write", inlineMutationSegment{
		Name:  "content",
		Value: p.Content,
	}); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	resolvedPath := w.resolvePath(p.FilePath)

	if err := w.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	// 转换为绝对路径
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("解析文件路径失败: %w", err),
		}, nil
	}

	// 检查文件是否已存在
	fileExists := false
	var oldSize int64 = 0
	oldContent := ""
	if fileInfo, err := os.Stat(absPath); err == nil {
		fileExists = true
		oldSize = fileInfo.Size()
		if fileInfo.IsDir() {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      w.buildPathKindMismatchError("路径是目录，不是文件", p.FilePath),
			}, nil
		}
		if !fileInfo.Mode().IsRegular() {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      fmt.Errorf("路径已存在但不是常规文件: %s", absPath),
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
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("创建目录失败: %w", err),
		}, nil
	}

	// 写入文件
	err = os.WriteFile(absPath, []byte(p.Content), 0644)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("写入文件失败: %w", err),
		}, nil
	}

	// 获取新文件信息
	newInfo, err := os.Stat(absPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("获取文件信息失败: %w", err),
		}, nil
	}

	action := "创建"
	if fileExists {
		action = "覆盖"
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    fmt.Sprintf("成功%s文件: %s\n文件大小: %d 字节", action, absPath, newInfo.Size()),
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
