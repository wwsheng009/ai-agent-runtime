package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/filetransport"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// AppendWriteTool appends one chunk of text to a file, with optional truncate-first
// behavior for the first chunk of a long generation.
type AppendWriteTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	transport filetransport.Service
}

// NewAppendWriteTool creates a chunk-friendly append tool for long text writes.
func NewAppendWriteTool() *AppendWriteTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要写入的文件路径。一次只处理一个目标文件。",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "本次要追加或写入的单个文本块。适合长文本分块落盘；请多次调用，每次只发送一个较小块。",
			},
			"truncate_first": map[string]interface{}{
				"type":        "boolean",
				"description": "若为 true，则先清空/覆盖文件，再写入本块；适合第一块写入。默认 false。",
			},
			"ensure_trailing_newline": map[string]interface{}{
				"type":        "boolean",
				"description": "若为 true 且本块末尾没有换行，则自动补一个换行。默认 false。",
			},
		},
		"required": []string{"file_path", "content"},
	}

	return &AppendWriteTool{
		BaseTool: toolkit.NewBaseTool(
			"append_write",
			"按块追加写入文件；适合长文本、长代码、章节内容等分块落盘。第一块可用 truncate_first=true，后续块继续调用 append_write，避免单次 write(content=全文) 导致工具参数过大或输出截断。",
			"1.0.0",
			parameters,
			true,
		),
		transport: filetransport.NewLocalService(),
	}
}

func (w *AppendWriteTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: false,
	}
}

type AppendWriteParams struct {
	FilePath              string `json:"file_path"`
	Content               string `json:"content"`
	TruncateFirst         bool   `json:"truncate_first"`
	EnsureTrailingNewline bool   `json:"ensure_trailing_newline"`
}

func (w *AppendWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if result, truncated := truncatedToolArgsResult(params); truncated {
		return result, nil
	}

	filePath, ok := params["file_path"].(string)
	if !ok || strings.TrimSpace(filePath) == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("file_path 参数缺失或无效"),
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("content 参数缺失或无效"),
		}, nil
	}
	if err := validateInlineFileMutationPayload("append_write", inlineMutationSegment{
		Name:  "content",
		Value: content,
	}); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	truncateFirst, _ := params["truncate_first"].(bool)
	ensureTrailingNewline, _ := params["ensure_trailing_newline"].(bool)
	if ensureTrailingNewline && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	resolvedPath := w.resolvePath(filePath)
	if err := w.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("解析文件路径失败: %w", err),
		}, nil
	}

	oldContent := ""
	if data, readErr := os.ReadFile(absPath); readErr == nil {
		oldContent = string(data)
	}

	var transferResult *filetransport.WriteResult
	if truncateFirst {
		transferResult, err = w.transport.WriteFile(ctx, absPath, []byte(content))
	} else {
		transferResult, err = w.transport.AppendFile(ctx, absPath, []byte(content))
	}
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	newContentBytes, err := os.ReadFile(absPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取写入后文件失败: %w", err),
		}, nil
	}
	newContent := string(newContentBytes)

	action := transferResult.Action
	if truncateFirst && transferResult.Created {
		action = "create_first_chunk"
	} else if truncateFirst {
		action = "overwrite_first_chunk"
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    fmt.Sprintf("成功写入块到文件: %s\n本次写入: %d 字节\n文件当前大小: %d 字节", transferResult.Path, transferResult.BytesWritten, transferResult.SizeAfter),
		Metadata: map[string]interface{}{
			"file_path":         transferResult.Path,
			"action":            action,
			"bytes_written":     transferResult.BytesWritten,
			"size_before":       transferResult.SizeBefore,
			"size_after":        transferResult.SizeAfter,
			"truncate_first":    truncateFirst,
			"appended":          !truncateFirst,
			"patch":             buildUnifiedPatch(transferResult.Path, oldContent, newContent),
			"mutated_paths":     []string{transferResult.Path},
			"transport_backend": "local_filetransport",
		},
	}, nil
}
