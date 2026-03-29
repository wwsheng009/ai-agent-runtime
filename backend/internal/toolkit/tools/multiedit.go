package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

// MultieditTool 多处编辑工具
type MultieditTool struct {
	*toolkit.BaseTool
	sandboxPolicy
}

// EditOperation 单个编辑操作
type EditOperation struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// NewMultieditTool 创建多处编辑工具
func NewMultieditTool() *MultieditTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要编辑的文件绝对路径",
			},
			"edits": map[string]interface{}{
				"type":        "array",
				"description": "编辑操作数组，每个操作包含 old_string（要替换的文本）、new_string（替换后的文本）、replace_all（是否替换所有匹配，默认 false）",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old_string": map[string]interface{}{
							"type":        "string",
							"description": "要替换的文本（必须精确匹配，包括空格和缩进）",
						},
						"new_string": map[string]interface{}{
							"type":        "string",
							"description": "替换后的文本",
						},
						"replace_all": map[string]interface{}{
							"type":        "boolean",
							"description": "是否替换所有匹配项（默认 false）",
						},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"file_path", "edits"},
	}

	return &MultieditTool{
		BaseTool: toolkit.NewBaseTool(
			"multiedit",
			"在单个文件中执行多次文本替换操作。按顺序应用每个编辑，后续编辑基于前面编辑的结果。",
			"1.0.0",
			parameters,
			true,
		),
	}
}

// Execute 实现 Tool 接口
func (m *MultieditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析文件路径
	filePath, ok := params["file_path"].(string)
	if !ok || filePath == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("file_path 参数缺失或为空"),
		}, nil
	}

	// 解析编辑操作
	editsRaw, ok := params["edits"].([]interface{})
	if !ok || len(editsRaw) == 0 {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("edits 参数缺失或为空数组"),
		}, nil
	}

	// 解析每个编辑操作
	edits := make([]EditOperation, 0, len(editsRaw))
	for i, editRaw := range editsRaw {
		editMap, ok := editRaw.(map[string]interface{})
		if !ok {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("edits[%d] 不是有效的对象", i),
			}, nil
		}

		oldStr, ok := editMap["old_string"].(string)
		if !ok || oldStr == "" {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("edits[%d].old_string 缺失或为空", i),
			}, nil
		}

		newStr, ok := editMap["new_string"].(string)
		if !ok {
			newStr = ""
		}

		replaceAll := false
		if ra, ok := editMap["replace_all"].(bool); ok {
			replaceAll = ra
		}

		edits = append(edits, EditOperation{
			OldString:  oldStr,
			NewString:  newStr,
			ReplaceAll: replaceAll,
		})
	}
	resolvedPath := m.resolvePath(filePath)

	if err := m.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	// 读取文件内容
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法解析文件路径: %w", err),
		}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法读取文件: %w", err),
		}, nil
	}

	originalContent := string(content)
	result := originalContent
	appliedEdits := 0
	failedEdits := make([]string, 0)

	// 按顺序应用每个编辑操作
	for i, edit := range edits {
		// 检查是否存在匹配
		if !strings.Contains(result, edit.OldString) {
			failedEdits = append(failedEdits, fmt.Sprintf("编辑 %d: 未找到匹配文本", i))
			continue
		}

		if edit.ReplaceAll {
			// 替换所有匹配
			count := strings.Count(result, edit.OldString)
			result = strings.ReplaceAll(result, edit.OldString, edit.NewString)
			appliedEdits += count
		} else {
			// 只替换第一个匹配
			result = strings.Replace(result, edit.OldString, edit.NewString, 1)
			appliedEdits++
		}
	}

	// 检查是否有变化
	if result == originalContent {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("没有任何编辑被应用"),
		}, nil
	}

	// 写回文件
	if err := os.WriteFile(absPath, []byte(result), 0644); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("无法写入文件: %w", err),
		}, nil
	}

	// 计算统计信息
	linesBefore := len(strings.Split(originalContent, "\n"))
	linesAfter := len(strings.Split(result, "\n"))

	// 构建结果消息
	message := fmt.Sprintf("成功应用 %d 处编辑", appliedEdits)
	if len(failedEdits) > 0 {
		message += fmt.Sprintf("，%d 处失败: %s", len(failedEdits), strings.Join(failedEdits, "; "))
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: message,
		Metadata: map[string]interface{}{
			"file_path":       absPath,
			"edits_applied":   appliedEdits,
			"edits_failed":    len(failedEdits),
			"lines_before":    linesBefore,
			"lines_after":     linesAfter,
			"size_before":     len(originalContent),
			"size_after":      len(result),
			"size_difference": len(result) - len(originalContent),
			"patch":           buildUnifiedPatch(absPath, originalContent, result),
			"mutated_paths":   []string{absPath},
		},
	}, nil
}
