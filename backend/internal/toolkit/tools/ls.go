package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

// LsTool 列出目录工具
type LsTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxDepth int
	limit    int
}

type lsEntry struct {
	relPath string
	depth   int
	isDir   bool
}

// NewLsTool 创建 Ls 工具
func NewLsTool() *LsTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "目录路径（默认为当前目录）",
			},
			"depth": map[string]interface{}{
				"type":        "integer",
				"description": "递归深度（默认为 1，只显示当前目录）",
			},
		},
		"required": []string{},
	}

	return &LsTool{
		BaseTool: toolkit.NewBaseTool(
			"ls",
			"列出目录内容",
			"1.0.0",
			parameters,
			true,
		),
		maxDepth: 10,
		limit:    1000,
	}
}

// Execute 实现 Tool 接口
func (l *LsTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	// 解析路径
	path := "."
	if p, ok := params["path"].(string); ok && p != "" {
		path = p
	}
	resolvedPath := l.resolvePath(path)
	if err := l.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	// 解析深度
	depth := 1
	if d, ok := params["depth"].(float64); ok {
		depth = int(d)
	}
	if d, ok := params["depth"].(int); ok {
		depth = d
	}
	if d, ok := params["depth"].(int64); ok {
		depth = int(d)
	}
	if depth > l.maxDepth {
		depth = l.maxDepth
	}

	// 检查路径是否存在
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("路径不存在: %w", err),
		}, nil
	}

	if !info.IsDir() {
		// 如果是文件，返回文件信息
		return &toolkit.ToolResult{
			Success: true,
			Content: fmt.Sprintf("%s (%d bytes, %s)", path, info.Size(), info.Mode().String()),
			Metadata: map[string]interface{}{
				"path":  path,
				"isDir": false,
				"size":  info.Size(),
				"mode":  info.Mode().String(),
			},
		}, nil
	}

	// 收集目录内容
	entries := make([]lsEntry, 0)
	fileCount := 0
	dirCount := 0

	err = filepath.Walk(resolvedPath, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// 计算相对路径
		relPath, err := filepath.Rel(resolvedPath, walkPath)
		if err != nil {
			relPath = walkPath
		}

		// 跳过根目录
		if relPath == "." {
			return nil
		}

		// 检查深度
		depthCount := strings.Count(relPath, string(filepath.Separator))
		if depthCount >= depth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// 添加到结果
		normalizedRelPath := filepath.ToSlash(relPath)
		if info.IsDir() {
			entries = append(entries, lsEntry{relPath: normalizedRelPath, depth: depthCount, isDir: true})
			dirCount++
		} else {
			entries = append(entries, lsEntry{relPath: normalizedRelPath, depth: depthCount, isDir: false})
			fileCount++
		}

		// 检查限制
		if len(entries) >= l.limit {
			return fmt.Errorf("limit reached")
		}

		return nil
	})

	// 如果是限制错误，忽略
	if err != nil && err.Error() != "limit reached" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("遍历目录失败: %w", err),
		}, nil
	}

	// 排序（目录在前，文件在后）
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].relPath == entries[j].relPath {
			if entries[i].isDir == entries[j].isDir {
				return entries[i].depth < entries[j].depth
			}
			return entries[i].isDir && !entries[j].isDir
		}
		return entries[i].relPath < entries[j].relPath
	})

	// 格式化输出
	var output strings.Builder
	output.WriteString(fmt.Sprintf("目录: %s\n\n", path))

	if len(entries) == 0 {
		output.WriteString("(空目录)")
	} else {
		for _, entry := range entries {
			prefix := strings.Repeat("  ", entry.depth)
			if entry.isDir {
				output.WriteString(fmt.Sprintf("%s📁 %s/\n", prefix, entry.relPath))
			} else {
				output.WriteString(fmt.Sprintf("%s📄 %s\n", prefix, entry.relPath))
			}
		}
	}

	output.WriteString(fmt.Sprintf("\n统计: %d 个文件, %d 个目录", fileCount, dirCount))

	return &toolkit.ToolResult{
		Success: true,
		Content: output.String(),
		Metadata: map[string]interface{}{
			"path":       path,
			"depth":      depth,
			"file_count": fileCount,
			"dir_count":  dirCount,
			"total":      len(entries),
		},
	}, nil
}
