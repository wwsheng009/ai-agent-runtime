package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

var errGrepLimitReached = errors.New("grep match limit reached")

// GrepTool 文件内容搜索工具
type GrepTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxMatches int
	lookPath   func(string) (string, error)
	runCommand func(context.Context, string, string, []string) ([]byte, error)
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
			"文件内容搜索（优先使用 ripgrep/rg，不可用时回退到内置扫描）",
			"1.1.0",
			parameters,
			true,
		),
		maxMatches: 100,
		lookPath:   exec.LookPath,
		runCommand: runGrepCommand,
	}
}

// Execute 实现 Tool 接口
func (g *GrepTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("pattern 参数缺失或无效"),
		}, nil
	}

	searchPath := "."
	if path, ok := params["path"].(string); ok && path != "" {
		searchPath = path
	}
	resolvedSearchPath := g.resolvePath(searchPath)
	if err := g.checkPath(runtimeexecutor.OpRead, resolvedSearchPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	includePattern := ""
	if inc, ok := params["include"].(string); ok && inc != "" {
		includePattern = inc
	}

	literal := resolveLiteralSearchParam(params)

	re, err := compileGrepPattern(pattern, literal)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if result, used, err := g.searchWithRipgrep(ctx, pattern, searchPath, resolvedSearchPath, includePattern, literal); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	} else if used {
		return result, nil
	}

	return g.searchWithWalker(resolvedSearchPath, searchPath, pattern, includePattern, literal, re), nil
}

func compileGrepPattern(pattern string, literal bool) (*regexp.Regexp, error) {
	if literal {
		return regexp.MustCompile(regexp.QuoteMeta(pattern)), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("正则表达式无效: %w", err)
	}
	return re, nil
}

func resolveLiteralSearchParam(params map[string]interface{}) bool {
	if literal, ok := params["literal"].(bool); ok {
		return literal
	}
	if literal, ok := params["literal_text"].(bool); ok {
		return literal
	}
	return false
}

func (g *GrepTool) searchWithRipgrep(ctx context.Context, pattern, searchPath, resolvedSearchPath, includePattern string, literal bool) (*toolkit.ToolResult, bool, error) {
	if g == nil || g.lookPath == nil || g.runCommand == nil {
		return nil, false, nil
	}
	rgPath, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(rgPath) == "" {
		return nil, false, nil
	}

	args := buildRipgrepArgs(pattern, includePattern, literal, g.maxMatches)
	output, err := g.runCommand(ctx, rgPath, resolvedSearchPath, args)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		if isRipgrepNoMatch(err) {
			return buildGrepResult(searchPath, pattern, includePattern, literal, nil, 0, false, "rg"), true, nil
		}
		return nil, false, nil
	}

	lines := normalizeRipgrepOutput(output)
	matchCount := len(lines)
	truncated := false
	if matchCount > g.maxMatches {
		lines = lines[:g.maxMatches]
		truncated = true
	}
	return buildGrepResult(searchPath, pattern, includePattern, literal, lines, matchCount, truncated, "rg"), true, nil
}

func buildRipgrepArgs(pattern, includePattern string, literal bool, maxMatches int) []string {
	args := []string{
		"--line-number",
		"--with-filename",
		"--color=never",
		"--no-heading",
		"--hidden",
		"--no-ignore",
		"--glob", "!.git/**",
		"--glob", "!node_modules/**",
	}
	if maxMatches > 0 {
		args = append(args, "--max-count", fmt.Sprintf("%d", maxMatches))
	}
	if includePattern != "" {
		args = append(args, "--glob", filepath.ToSlash(includePattern))
	}
	if literal {
		args = append(args, "-F")
	}
	args = append(args, "--", pattern)
	return args
}

func normalizeRipgrepOutput(output []byte) []string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n"))
	if trimmed == "" {
		return nil
	}
	rawLines := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 {
			line = fmt.Sprintf("%s:%s: %s", parts[0], parts[1], parts[2])
		}
		lines = append(lines, line)
	}
	return lines
}

func isRipgrepNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func runGrepCommand(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workingDir
	return cmd.CombinedOutput()
}

func (g *GrepTool) searchWithWalker(resolvedSearchPath, searchPath, pattern, includePattern string, literal bool, re *regexp.Regexp) *toolkit.ToolResult {
	results := make([]string, 0, 16)
	matchCount := 0

	err := filepath.Walk(resolvedSearchPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info == nil {
			return nil
		}
		if info.IsDir() {
			if shouldSkipGrepDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if includePattern != "" {
			matched, err := filepath.Match(includePattern, filepath.Base(path))
			if err != nil || !matched {
				return nil
			}
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}

		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if !re.MatchString(line) {
				continue
			}

			relPath, err := filepath.Rel(resolvedSearchPath, path)
			if err != nil {
				relPath = path
			}
			results = append(results, fmt.Sprintf("%s:%d: %s", relPath, lineNum, line))
			matchCount++
			if matchCount >= g.maxMatches {
				_ = file.Close()
				return errGrepLimitReached
			}
		}

		_ = file.Close()
		return nil
	})
	if err != nil && !errors.Is(err, errGrepLimitReached) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	truncated := errors.Is(err, errGrepLimitReached)
	return buildGrepResult(searchPath, pattern, includePattern, literal, results, matchCount, truncated, "builtin")
}

func shouldSkipGrepDir(name string) bool {
	switch strings.TrimSpace(name) {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

func buildGrepResult(searchPath, pattern, includePattern string, literal bool, results []string, matchCount int, truncated bool, engine string) *toolkit.ToolResult {
	output := "未找到匹配的内容"
	if len(results) > 0 {
		output = strings.Join(results, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个匹配)", len(results))
		}
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata: map[string]interface{}{
			"pattern":     pattern,
			"path":        searchPath,
			"include":     includePattern,
			"literal":     literal,
			"match_count": matchCount,
			"truncated":   truncated,
			"engine":      engine,
		},
	}
}
