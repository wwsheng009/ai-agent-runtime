package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// GrepTool 文件内容搜索工具
type GrepTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxMatches int
}

// NewGrepTool 创建 Grep 工具
func NewGrepTool() *GrepTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "搜索模式（正则表达式或字面文本）",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索路径（默认为当前目录）",
			},
			"include": map[string]interface{}{
				"type":        "string",
				"description": "文件名模式过滤，例如 *.go（默认搜索所有文件）",
			},
			"literal": map[string]interface{}{
				"type":        "boolean",
				"description": "是否为字面文本搜索（默认为 false，使用正则表达式）",
			},
		},
		"required": []string{"pattern"},
	}

	return &GrepTool{
		BaseTool: toolkit.NewBaseTool(
			"grep",
			"文件内容搜索",
			"1.0.0",
			parameters,
			true,
		),
		maxMatches: 100,
	}
}

// Execute 实现 Tool 接口
func (g *GrepTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
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

	includePattern := ""
	if inc, ok := params["include"].(string); ok && inc != "" {
		includePattern = inc
	}

	literal := false
	if l, ok := params["literal"].(bool); ok {
		literal = l
	}

	// 编译正则表达式
	var re *regexp.Regexp
	var err error
	if literal {
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	} else {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return &toolkit.ToolResult{
				Success: false,
				Error:   fmt.Errorf("正则表达式无效: %w", err),
			}, nil
		}
	}

	// 搜索文件
	results := []string{}
	matchCount := 0

	err = filepath.Walk(resolvedSearchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// 跳过二进制文件和常见排除目录
		if strings.Contains(path, ".git") || strings.Contains(path, "node_modules") {
			return nil
		}

		// 文件名模式过滤
		if includePattern != "" {
			matched, err := filepath.Match(includePattern, filepath.Base(path))
			if err != nil || !matched {
				return nil
			}
		}

		// 读取文件内容
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			if re.MatchString(line) {
				relPath, err := filepath.Rel(resolvedSearchPath, path)
				if err != nil {
					relPath = path
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", relPath, lineNum, line))
				matchCount++

				if matchCount >= g.maxMatches {
					return nil
				}
			}
		}

		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success: false,
			Error:   fmt.Errorf("搜索失败: %w", err),
		}, nil
	}

	// 格式化输出
	var output string
	if len(results) == 0 {
		output = "未找到匹配的内容"
	} else {
		output = strings.Join(results, "\n")
		if matchCount >= g.maxMatches {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个匹配)", g.maxMatches)
		}
	}

	return &toolkit.ToolResult{
		Success: true,
		Content: output,
		Metadata: map[string]interface{}{
			"pattern":     pattern,
			"path":        searchPath,
			"include":     includePattern,
			"literal":     literal,
			"match_count": matchCount,
			"truncated":   matchCount >= g.maxMatches,
		},
	}, nil
}
