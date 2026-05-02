package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	defaultGlobLimit = 100
	maxGlobLimit     = 1000
)

// GlobTool 文件名模式匹配工具
type GlobTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	limit      int
	lookPath   func(string) (string, error)
	runCommand func(context.Context, string, string, []string) ([]byte, error)
}

// NewGlobTool 创建 Glob 工具
func NewGlobTool() *GlobTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "文件名/路径 glob 模式，例如 *.go, **/*.yaml。glob 只匹配路径，不搜索文件内容；若要查内容请使用 grep。若需要忽略大小写，优先使用 case_insensitive=true，而不是并行构造多个大小写变体。",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索路径（默认为当前目录）。先把 path 缩小到最可能的子目录，再使用 glob，可避免不必要的全仓 ** 扫描。",
			},
			"case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "路径匹配是否忽略大小写。用于查找 BotPage/botpage 等大小写不确定的文件名，避免重复发多个大小写变体 glob。",
				"default":     false,
			},
			"ignore_case": map[string]interface{}{
				"type":        "boolean",
				"description": "case_insensitive 的兼容别名。",
				"default":     false,
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "最多返回的匹配数量（默认为 100，最大 1000）",
				"default":     defaultGlobLimit,
				"maximum":     maxGlobLimit,
			},
		},
		"required": []string{"pattern"},
	}

	return &GlobTool{
		BaseTool: toolkit.NewBaseTool(
			"glob",
			"文件名/路径模式匹配搜索，不搜索文件内容。递归文件匹配会优先使用 rg --files 加速；目录匹配、单层匹配或 rg 不可用时回退内置遍历。大小写不确定时使用 case_insensitive=true。",
			"1.0.0",
			parameters,
			true,
		),
		limit:      defaultGlobLimit,
		lookPath:   exec.LookPath,
		runCommand: runGrepCommand,
	}
}

func (g *GlobTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataSupportsParallelKey: true,
	}
}

// Execute 实现 Tool 接口
func (g *GlobTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
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
	searchPathInfo, err := os.Stat(resolvedSearchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      g.buildPathNotFoundError("搜索路径不可用", searchPath),
			}, nil
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索路径不可用: %w", err),
		}, nil
	}
	if err := validateRelativePattern(pattern); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	limit := g.limit
	if limit <= 0 {
		limit = defaultGlobLimit
	}
	if rawLimit, ok := params["limit"]; ok && rawLimit != nil {
		parsedLimit, err := parseGlobLimit(rawLimit)
		if err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
		limit = parsedLimit
	}
	caseInsensitive, _ := resolveBoolParam(params, "case_insensitive", "ignore_case")

	matches, truncated, engine, err := g.findMatches(ctx, resolvedSearchPath, pattern, searchPathInfo.IsDir(), limit, caseInsensitive)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("glob 匹配失败: %w", err),
		}, nil
	}

	// 格式化输出
	var output string
	if len(matches) == 0 {
		output = "未找到匹配项"
	} else {
		output = strings.Join(matches, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个文件)", limit)
		}
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata: map[string]interface{}{
			"pattern":          pattern,
			"path":             searchPath,
			"limit":            limit,
			"case_insensitive": caseInsensitive,
			"count":            len(matches), // 兼容字段：返回数量
			"returned_count":   len(matches),
			"files":            append([]string(nil), matches...),
			"truncated":        truncated, // 兼容字段：是否被截断
			"limit_hit":        truncated,
			"engine":           engine,
		},
	}, nil
}

func (g *GlobTool) findMatches(ctx context.Context, resolvedSearchPath, pattern string, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, string, error) {
	compiled := compileGlobPattern(pattern)
	if compiled.normalized == "" {
		return nil, false, "builtin", nil
	}
	if matches, handled, err := g.findExactMatches(resolvedSearchPath, compiled, rootIsDir, caseInsensitive); handled || err != nil {
		return matches, false, "builtin", err
	}
	if matches, truncated, used, err := g.findMatchesWithRipgrep(ctx, resolvedSearchPath, compiled, rootIsDir, limit, caseInsensitive); err != nil {
		return nil, false, "rg", err
	} else if used {
		return matches, truncated, "rg", nil
	}
	if rootIsDir && len(compiled.parts) == 1 && compiled.parts[0] != "**" {
		matches, truncated, err := g.findMatchesInCurrentDir(resolvedSearchPath, compiled, limit, caseInsensitive)
		return matches, truncated, "builtin", err
	}
	walkRoot := resolvedSearchPath
	walkPrefixParts := make([]string, 0, len(compiled.parts))
	if rootIsDir && !caseInsensitive {
		if prefix := compiled.staticPrefix; prefix != "" {
			candidateRoot := filepath.Join(resolvedSearchPath, filepath.FromSlash(prefix))
			if _, err := os.Stat(candidateRoot); err != nil {
				if os.IsNotExist(err) {
					return nil, false, "builtin", nil
				}
				return nil, false, "builtin", err
			}
			walkRoot = candidateRoot
			walkPrefixParts = splitGlobSegments(prefix)
		}
	}
	matches := make([]string, 0, 16)
	truncated, err := g.walkGlobTree(walkRoot, walkPrefixParts, compiled, &matches, limit, caseInsensitive)
	if err != nil {
		return nil, false, "builtin", err
	}
	return matches, truncated, "builtin", nil
}

func (g *GlobTool) findMatchesWithRipgrep(ctx context.Context, resolvedSearchPath string, compiled compiledGlobPattern, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, bool, error) {
	if !shouldUseRipgrepGlob(rootIsDir, compiled) {
		return nil, false, false, nil
	}
	if g == nil || g.lookPath == nil || g.runCommand == nil {
		return nil, false, false, nil
	}
	rgPath, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(rgPath) == "" {
		return nil, false, false, nil
	}

	globFlag := "--glob"
	if caseInsensitive {
		globFlag = "--iglob"
	}
	args := []string{"--files", "--hidden", "--no-ignore", globFlag, compiled.normalized}
	output, err := g.runCommand(ctx, rgPath, resolvedSearchPath, args)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, true, ctxErr
		}
		if isRipgrepNoMatch(err) {
			return nil, false, true, nil
		}
		return nil, false, false, nil
	}

	matches := make([]string, 0, 16)
	truncated := false
	for _, rawLine := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		normalized := normalizeGlobPattern(line)
		matched, err := compiled.matchCandidate(splitGlobSegments(normalized), caseInsensitive)
		if err != nil {
			return nil, false, true, err
		}
		if !matched {
			continue
		}
		if limit > 0 && len(matches) >= limit {
			truncated = true
			break
		}
		matches = append(matches, filepath.FromSlash(normalized))
	}
	return matches, truncated, true, nil
}

func (g *GlobTool) walkGlobTree(absDir string, relParts []string, compiled compiledGlobPattern, matches *[]string, limit int, caseInsensitive bool) (bool, error) {
	if limit > 0 && len(*matches) >= limit {
		return true, nil
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if limit > 0 && len(*matches) >= limit {
			return true, nil
		}
		name := entry.Name()
		relParts = append(relParts, name)
		matched, err := compiled.matchCandidate(relParts, caseInsensitive)
		if err != nil {
			relParts = relParts[:len(relParts)-1]
			return false, err
		}
		canDescend := entry.IsDir() && canDescendGlobParts(compiled, relParts, caseInsensitive)
		if matched {
			*matches = append(*matches, filepath.Join(relParts...))
			if limit > 0 && len(*matches) >= limit {
				relParts = relParts[:len(relParts)-1]
				return true, nil
			}
		}
		if entry.IsDir() && canDescend {
			nextDir := filepath.Join(absDir, name)
			truncated, err := g.walkGlobTree(nextDir, relParts, compiled, matches, limit, caseInsensitive)
			relParts = relParts[:len(relParts)-1]
			if err != nil {
				return false, err
			}
			if truncated {
				return true, nil
			}
			continue
		}
		relParts = relParts[:len(relParts)-1]
	}
	return false, nil
}

func (c compiledGlobPattern) matchCandidate(pathParts []string, caseInsensitive bool) (bool, error) {
	if c.leadingDoubleStar {
		return matchLeadingDoubleStarTail(c.recursiveTail, pathParts, caseInsensitive)
	}
	return matchGlobSegmentsWithCase(c.parts, pathParts, caseInsensitive)
}

func (g *GlobTool) findMatchesInCurrentDir(resolvedSearchPath string, compiled compiledGlobPattern, limit int, caseInsensitive bool) ([]string, bool, error) {
	entries, err := os.ReadDir(resolvedSearchPath)
	if err != nil {
		return nil, false, err
	}
	matches := make([]string, 0, 16)
	for _, entry := range entries {
		matched, err := matchGlobPart(compiled.parts[0], entry.Name(), caseInsensitive)
		if err != nil {
			return nil, false, err
		}
		if matched {
			matches = append(matches, entry.Name())
			if limit > 0 && len(matches) >= limit {
				return matches, true, nil
			}
		}
	}
	return matches, false, nil
}

func (g *GlobTool) findExactMatches(resolvedSearchPath string, compiled compiledGlobPattern, rootIsDir bool, caseInsensitive bool) ([]string, bool, error) {
	if !rootIsDir {
		baseName := filepath.Base(resolvedSearchPath)
		matched, err := matchGlobSegmentsWithCase(compiled.parts, splitGlobSegments(baseName), caseInsensitive)
		if err != nil {
			return nil, true, err
		}
		if matched {
			return []string{baseName}, true, nil
		}
		return nil, true, nil
	}
	if !compiled.hasMeta && !caseInsensitive {
		if compiled.normalized == "." {
			return nil, true, nil
		}
		candidatePath := filepath.Join(resolvedSearchPath, filepath.FromSlash(compiled.normalized))
		if _, err := os.Stat(candidatePath); err != nil {
			if os.IsNotExist(err) {
				return nil, true, nil
			}
			return nil, true, err
		}
		return []string{filepath.FromSlash(compiled.normalized)}, true, nil
	}
	return nil, false, nil
}

type compiledGlobPattern struct {
	normalized        string
	parts             []string
	hasMeta           bool
	staticPrefix      string
	deepTraversal     bool
	leadingDoubleStar bool
	recursiveTail     []string
}

func compileGlobPattern(pattern string) compiledGlobPattern {
	normalized := normalizeGlobPattern(pattern)
	parts := splitGlobSegments(normalized)
	hasMeta := false
	deepTraversal := false
	leadingDoubleStar := false
	var recursiveTail []string
	prefix := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "**" {
			deepTraversal = true
			break
		}
		if hasGlobMeta(part) {
			hasMeta = true
			break
		}
		prefix = append(prefix, part)
	}
	if !hasMeta {
		for _, part := range parts {
			if hasGlobMeta(part) {
				hasMeta = true
				break
			}
		}
	}
	if len(parts) > 0 && parts[0] == "**" && !containsDoubleStar(parts[1:]) {
		leadingDoubleStar = true
		if len(parts) > 1 {
			recursiveTail = append([]string(nil), parts[1:]...)
		}
	}
	return compiledGlobPattern{
		normalized:        normalized,
		parts:             parts,
		hasMeta:           hasMeta,
		staticPrefix:      strings.Join(prefix, "/"),
		deepTraversal:     deepTraversal,
		leadingDoubleStar: leadingDoubleStar,
		recursiveTail:     recursiveTail,
	}
}

func shouldUseRipgrepGlob(rootIsDir bool, compiled compiledGlobPattern) bool {
	if !rootIsDir || compiled.normalized == "" || !compiled.hasMeta {
		return false
	}
	if len(compiled.parts) == 1 && compiled.parts[0] != "**" {
		return false
	}
	last := compiled.parts[len(compiled.parts)-1]
	if last == "**" || last == "" {
		return false
	}
	return strings.Contains(last, ".")
}

func canDescendGlobParts(compiled compiledGlobPattern, relParts []string, caseInsensitive bool) bool {
	if compiled.deepTraversal {
		return true
	}
	if len(relParts) >= len(compiled.parts) {
		return false
	}
	for i, part := range relParts {
		matched, err := matchGlobPart(compiled.parts[i], part, caseInsensitive)
		if err != nil || !matched {
			return false
		}
	}
	return true
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
	return matchGlobSegmentsWithCase(patternParts, pathParts, false)
}

func matchGlobSegmentsWithCase(patternParts, pathParts []string, caseInsensitive bool) (bool, error) {
	if !containsDoubleStar(patternParts) {
		if len(patternParts) != len(pathParts) {
			return false, nil
		}
		for i := range patternParts {
			matched, err := matchGlobPart(patternParts[i], pathParts[i], caseInsensitive)
			if err != nil || !matched {
				return false, err
			}
		}
		return true, nil
	}

	type matchState struct {
		patternIndex int
		pathIndex    int
	}
	memo := make(map[matchState]bool)
	var match func(int, int) (bool, error)
	match = func(patternIndex, pathIndex int) (bool, error) {
		state := matchState{patternIndex: patternIndex, pathIndex: pathIndex}
		if cached, ok := memo[state]; ok {
			return cached, nil
		}
		var result bool
		defer func() {
			memo[state] = result
		}()

		if patternIndex >= len(patternParts) {
			result = pathIndex >= len(pathParts)
			return result, nil
		}
		current := patternParts[patternIndex]
		if current == "**" {
			if patternIndex == len(patternParts)-1 {
				result = true
				return true, nil
			}
			for skip := pathIndex; skip <= len(pathParts); skip++ {
				matched, err := match(patternIndex+1, skip)
				if err != nil {
					return false, err
				}
				if matched {
					result = true
					return true, nil
				}
			}
			result = false
			return false, nil
		}
		if pathIndex >= len(pathParts) {
			result = false
			return false, nil
		}
		matched, err := matchGlobPart(current, pathParts[pathIndex], caseInsensitive)
		if err != nil || !matched {
			result = false
			return false, err
		}
		return match(patternIndex+1, pathIndex+1)
	}
	return match(0, 0)
}

func matchLeadingDoubleStarTail(tailParts, pathParts []string, caseInsensitive bool) (bool, error) {
	if len(tailParts) == 0 {
		return true, nil
	}
	if len(pathParts) < len(tailParts) {
		return false, nil
	}
	start := len(pathParts) - len(tailParts)
	for i := range tailParts {
		matched, err := matchGlobPart(tailParts[i], pathParts[start+i], caseInsensitive)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

func matchGlobPattern(pattern, relPath string) (bool, error) {
	compiled := compileGlobPattern(pattern)
	if compiled.normalized == "" {
		return false, nil
	}
	return matchGlobSegments(compiled.parts, splitGlobSegments(normalizeGlobPattern(relPath)))
}

func matchGlobPart(patternPart, pathPart string, caseInsensitive bool) (bool, error) {
	if caseInsensitive {
		patternPart = strings.ToLower(patternPart)
		pathPart = strings.ToLower(pathPart)
	}
	return path.Match(patternPart, pathPart)
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func containsDoubleStar(parts []string) bool {
	for _, part := range parts {
		if part == "**" {
			return true
		}
	}
	return false
}

func staticGlobPrefix(pattern string) string {
	parts := splitGlobSegments(pattern)
	if len(parts) == 0 {
		return ""
	}
	prefix := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "**" || hasGlobMeta(part) {
			break
		}
		prefix = append(prefix, part)
	}
	return strings.Join(prefix, "/")
}

func parseGlobLimit(raw interface{}) (int, error) {
	var limit int
	switch v := raw.(type) {
	case int:
		limit = v
	case int8:
		limit = int(v)
	case int16:
		limit = int(v)
	case int32:
		limit = int(v)
	case int64:
		limit = int(v)
	case float32:
		limit = int(v)
	case float64:
		limit = int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("limit 参数无效")
		}
		limit = int(parsed)
	default:
		return 0, fmt.Errorf("limit 参数无效")
	}

	if limit <= 0 {
		return 0, fmt.Errorf("limit 参数必须大于 0")
	}
	if limit > maxGlobLimit {
		limit = maxGlobLimit
	}
	return limit, nil
}
