package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// EditTool 文件编辑工具（单处替换）
type EditTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	backupDir string
}

// NewEditTool 创建 Edit 工具
func NewEditTool() *EditTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要修改的文件绝对路径。若需要改写多个文件，请拆分为多次 edit 调用，每次只聚焦一个文件和一个替换目标。",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "要替换的文本（必须精确匹配，包括空格和换行）。若需要替换很长的片段或多个位置，请拆分为更小的定位块，避免单次参数过长导致工具调用被截断。",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "替换后的文本。若新内容较长，请拆分为多个更小的 edit/write 调用，每次只聚焦一个替换目标，按块逐步替换或重建。",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "是否替换所有匹配项（默认为 false，只替换第一处）",
			},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	}

	return &EditTool{
		BaseTool: toolkit.NewBaseTool(
			"edit",
			"编辑单个文件：使用 new_string 替换文件中的 old_string；适合小范围精确替换。若要改写多个文件或大段内容，请拆分为多个更小的 edit/write 调用，每次只聚焦一个文件和一个替换目标，按章节或按块逐步处理，避免单次参数过大导致截断。",
			"1.0.0",
			parameters,
			true,
		),
		backupDir: ".backups",
	}
}

type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Execute 实现 Tool 接口
func (e *EditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	var p EditParams

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

	oldString, ok := params["old_string"].(string)
	if !ok || oldString == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("old_string 参数缺失或无效"),
		}, nil
	}
	p.OldString = oldString

	newString, ok := params["new_string"].(string)
	if !ok {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("new_string 参数缺失或无效"),
		}, nil
	}
	p.NewString = newString
	if err := validateInlineFileMutationPayload(
		"edit",
		inlineMutationSegment{Name: "old_string", Value: p.OldString},
		inlineMutationSegment{Name: "new_string", Value: p.NewString},
	); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if replaceAll, ok := params["replace_all"].(bool); ok {
		p.ReplaceAll = replaceAll
	}
	resolvedPath := e.resolvePath(p.FilePath)

	if err := e.checkPath(runtimeexecutor.OpWrite, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	// 读取文件内容
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("解析文件路径失败: %w", err),
		}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("读取文件失败: %w", err),
		}, nil
	}

	contentStr := string(content)

	// 检查 old_string 是否存在
	if !strings.Contains(contentStr, p.OldString) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("old_string 未在文件中找到，请确保完全匹配（包括空格和换行）"),
		}, nil
	}

	// 创建备份
	backupPath, err := e.createBackup(absPath, content)
	if err != nil {
		// 备份失败不阻止编辑，只记录警告
		backupPath = ""
	}

	// 执行替换
	var newContent string
	var count int

	if p.ReplaceAll {
		// 替换所有匹配项
		newContent = strings.ReplaceAll(contentStr, p.OldString, p.NewString)
		count = strings.Count(contentStr, p.OldString)
	} else {
		// 只替换第一处
		newContent = strings.Replace(contentStr, p.OldString, p.NewString, 1)
		count = 1
	}

	// 写入文件
	err = os.WriteFile(absPath, []byte(newContent), 0644)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("写入文件失败: %w", err),
		}, nil
	}

	// 计算差异
	oldLen := len(contentStr)
	newLen := len(newContent)

	additions := 0
	removals := 0
	if newLen > oldLen {
		additions = newLen - oldLen
	} else {
		removals = oldLen - newLen
	}

	result := toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    fmt.Sprintf("成功替换了 %d 处匹配项", count),
		Metadata: map[string]interface{}{
			"file_path":     absPath,
			"replacements":  count,
			"additions":     additions,
			"removals":      removals,
			"old_size":      oldLen,
			"new_size":      newLen,
			"patch":         buildUnifiedPatch(absPath, contentStr, newContent),
			"mutated_paths": []string{absPath},
		},
	}

	if backupPath != "" {
		result.Metadata["backup_path"] = backupPath
	}

	return &result, nil
}

// createBackup 创建文件备份
func (e *EditTool) createBackup(filePath string, content []byte) (string, error) {
	// 获取绝对路径
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}

	// 创建备份目录
	backupDir := filepath.Join(filepath.Dir(absPath), e.backupDir)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}

	// 生成备份文件名（带时间戳）
	timestamp := time.Now().Format("20060102-150405")
	baseName := filepath.Base(absPath)
	backupName := fmt.Sprintf("%s.%s.bak", baseName, timestamp)
	backupPath := filepath.Join(backupDir, backupName)

	// 写入备份
	if err := os.WriteFile(backupPath, content, 0644); err != nil {
		return "", err
	}

	return backupPath, nil
}
