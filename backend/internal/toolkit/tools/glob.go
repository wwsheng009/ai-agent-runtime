package tools

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolkit"
)

// GlobTool 文件名模式匹配工具
type GlobTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	limit int
}

// NewGlobTool 创建 Glob 工具
func NewGlobTool() *GlobTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "glob 模式，例如 *.go, **/*.yaml",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索路径（默认为当前目录）",
			},
		},
		"required": []string{"pattern"},
	}

	return &GlobTool{
		BaseTool: toolkit.NewBaseTool(
			"glob",
			"文件名模式匹配搜索",
			"1.0.0",
			parameters,
			true,
		),
		limit: 100,
	}
}

// Execute 实现 Tool 接口
func (g *GlobTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("pattern 参数缺失或无效"),
		}, nil
	}

	searchPath := "."
	if path, ok := params["path"].(string); ok && path != "" {
		searchPath = path
	}
	resolvedSearchPath := g.resolvePath(searchPath)
	if err := g.checkPath(runtimeexecutor.OpRead, resolvedSearchPath); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}
	if err := validateRelativePattern(pattern); err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   err,
		}, nil
	}

	matches, err := g.findMatches(resolvedSearchPath, pattern)
	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("glob 匹配失败: %w", err),
		}, nil
	}
	sort.Strings(matches)

	// 限制结果数量
	truncated := false
	if len(matches) > g.limit {
		matches = matches[:g.limit]
		truncated = true
	}

	// 格式化输出
	var output string
	if len(matches) == 0 {
		output = "未找到匹配的文件"
	} else {
		output = strings.Join(matches, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个文件)", g.limit)
		}
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: output,
		Metadata: map[string]interface{}{
			"pattern":   pattern,
			"path":      searchPath,
			"count":     len(matches),
			"files":     append([]string(nil), matches...),
			"truncated": truncated,
		},
	}, nil
}

func (g *GlobTool) findMatches(resolvedSearchPath, pattern string) ([]string, error) {
	normalizedPattern := normalizeGlobPattern(pattern)
	matches := make([]string, 0, 16)
	seen := make(map[string]struct{})

	err := filepath.Walk(resolvedSearchPath, func(candidatePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		relPath, err := filepath.Rel(resolvedSearchPath, candidatePath)
		if err != nil {
			return nil
		}
		if relPath == "." {
			return nil
		}
		normalizedRel := filepath.ToSlash(relPath)
		matched, err := matchGlobPattern(normalizedPattern, normalizedRel)
		if err != nil || !matched {
			return nil
		}
		displayPath := filepath.FromSlash(normalizedRel)
		if _, exists := seen[displayPath]; exists {
			return nil
		}
		seen[displayPath] = struct{}{}
		matches = append(matches, displayPath)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func normalizeGlobPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	pattern = strings.ReplaceAll(pattern, `\`, `/`)
	for strings.HasPrefix(pattern, "./") {
		pattern = strings.TrimPrefix(pattern, "./")
	}
	for strings.HasPrefix(pattern, "/") {
		pattern = strings.TrimPrefix(pattern, "/")
	}
	return pattern
}

func matchGlobPattern(pattern, relPath string) (bool, error) {
	pattern = normalizeGlobPattern(pattern)
	relPath = normalizeGlobPattern(relPath)
	if pattern == "" {
		return false, nil
	}
	patternParts := splitGlobSegments(pattern)
	pathParts := splitGlobSegments(relPath)
	return matchGlobSegments(patternParts, pathParts)
}

func splitGlobSegments(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func matchGlobSegments(patternParts, pathParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(pathParts) == 0, nil
	}
	current := patternParts[0]
	if current == "**" {
		if len(patternParts) == 1 {
			return true, nil
		}
		for skip := 0; skip <= len(pathParts); skip++ {
			matched, err := matchGlobSegments(patternParts[1:], pathParts[skip:])
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}
	if len(pathParts) == 0 {
		return false, nil
	}
	matched, err := path.Match(current, pathParts[0])
	if err != nil || !matched {
		return false, err
	}
	return matchGlobSegments(patternParts[1:], pathParts[1:])
}
