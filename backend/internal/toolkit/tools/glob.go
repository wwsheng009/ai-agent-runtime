package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
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
				"description": "文件名/路径 glob 模式，例如 *.go, **/*.yaml。支持 * ? [] 与 **；常见 shell brace（如 *.{go,ts}）会自动展开为多个模式。多扩展名也可直接传 brace 或分多次调用。glob 只匹配路径，不搜索文件内容；若要查内容请使用 grep。大小写不确定时用 case_insensitive=true。",
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
			"文件名/路径模式匹配搜索，不搜索文件内容。支持 * ? [] **；常见 shell brace（*.{go,ts}）会自动展开。递归文件匹配优先用 rg --files；目录匹配、单层匹配或 rg 不可用时回退内置遍历。大小写不确定时用 case_insensitive=true。",
			"1.0.0",
			parameters,
			true,
		),
		limit:      defaultGlobLimit,
		lookPath:   runtimeripgrep.LookPath,
		runCommand: runGrepCommand,
	}
}

func (g *GlobTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindSearch,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
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

	expandedPatterns := expandShellBraceGlobs(pattern)
	if len(expandedPatterns) == 0 {
		expandedPatterns = []string{pattern}
	}
	braceExpanded := looksLikeUnsupportedBraceGlob(pattern) &&
		(len(expandedPatterns) > 1 || expandedPatterns[0] != strings.TrimSpace(pattern))
	// Residual brace that could not be expanded still needs recovery guidance.
	braceUnsupported := looksLikeUnsupportedBraceGlob(pattern) && !braceExpanded

	matches, truncated, engine, err := g.findMatchesMulti(ctx, resolvedSearchPath, expandedPatterns, searchPathInfo.IsDir(), limit, caseInsensitive)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("glob 匹配失败: %w", err),
		}, nil
	}

	// 格式化输出
	var output string
	braceHint := ""
	if braceUnsupported {
		braceHint = "（检测到无法安全展开的 shell brace 语法如 *.{a,b}；请拆成多次 pattern 调用，或改用 grep include/glob 数组。）"
	}
	if len(matches) == 0 {
		output = "未找到匹配项" + braceHint
	} else {
		output = strings.Join(matches, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个文件)", limit)
		}
	}

	metadata := map[string]interface{}{
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
	}
	backendCommand := "builtin-walker"
	backendPath := ""
	if engine == "rg" {
		backendCommand = "rg --files"
		if g != nil && g.lookPath != nil {
			resolved, resolveErr := g.lookPath("rg")
			if resolveErr == nil {
				backendPath = resolved
			}
		}
	}
	annotateSearchBackend(metadata, engine, backendCommand, backendPath)
	if braceExpanded {
		metadata["brace_expanded"] = true
		metadata["expanded_patterns"] = append([]string(nil), expandedPatterns...)
	}
	if braceUnsupported {
		metadata["unsupported_brace_pattern"] = true
	}
	// True no-match success: stamp empty disposition for model recovery
	// (broaden pattern / change path) without treating as hard failure.
	if len(matches) == 0 && !truncated {
		toolresult.MarkEmptySuccess(metadata)
		if braceUnsupported {
			metadata[toolresult.MetadataNextActionKey] = "glob 无法安全展开该 shell brace pattern（如过大或畸形 *.{go,ts}）。请拆成多次 pattern 调用（*.go、*.ts），或改用 toolkit grep 的 include/glob 数组。不要原样重试同一 brace pattern。"
		}
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata:   metadata,
	}, nil
}

// findMatchesMulti unions results across expanded brace patterns while
// respecting the shared limit and de-duplicating paths.
func (g *GlobTool) findMatchesMulti(ctx context.Context, resolvedSearchPath string, patterns []string, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, string, error) {
	if len(patterns) == 0 {
		return nil, false, "builtin", nil
	}
	if len(patterns) == 1 {
		return g.findMatches(ctx, resolvedSearchPath, patterns[0], rootIsDir, limit, caseInsensitive)
	}

	matches := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	engine := "builtin"
	for _, pattern := range patterns {
		// Request one extra across every alternative so an exact limit from an
		// early pattern does not falsely imply truncation when later patterns are empty.
		requestLimit := 0
		if limit > 0 {
			requestLimit = limit + 1
		}
		part, partTruncated, partEngine, err := g.findMatches(ctx, resolvedSearchPath, pattern, rootIsDir, requestLimit, caseInsensitive)
		if err != nil {
			return nil, false, partEngine, err
		}
		if partEngine != "" {
			engine = partEngine
		}
		for _, match := range part {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			matches = append(matches, match)
			if limit > 0 && len(matches) > limit {
				return matches[:limit], true, engine, nil
			}
		}
		if partTruncated {
			// The child search omitted results. Ordinarily requestLimit guarantees
			// enough returned rows to hit the shared limit; keep the flag defensive.
			if limit > 0 && len(matches) >= limit {
				return matches[:limit], true, engine, nil
			}
			return matches, true, engine, nil
		}
	}
	return matches, false, engine, nil
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

const maxShellBraceExpansion = 64

// expandShellBraceGlobs expands common shell brace patterns emitted by models
// (e.g. *.{go,ts} -> [*.go *.ts]). Nested braces expand left-to-right.
// Patterns without comma alternatives are returned unchanged.
func expandShellBraceGlobs(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	pending := []string{pattern}
	expanded := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		open, close := findExpandableBraceRange(current)
		if open < 0 {
			if _, ok := seen[current]; ok {
				continue
			}
			seen[current] = struct{}{}
			expanded = append(expanded, current)
			continue
		}

		alts := splitBraceAlternatives(current[open+1 : close])
		if len(alts) <= 1 {
			return []string{pattern}
		}
		// Fail closed instead of returning a partial expansion that silently
		// omits file types. The caller can keep the legacy recovery guidance.
		if len(expanded)+len(pending)+len(alts) > maxShellBraceExpansion {
			return []string{pattern}
		}
		prefix := current[:open]
		suffix := current[close+1:]
		for _, alt := range alts {
			pending = append(pending, prefix+alt+suffix)
		}
	}
	if len(expanded) == 0 {
		return []string{pattern}
	}
	return expanded
}

// findExpandableBraceRange returns the leftmost {...} span that contains a
// top-level comma (shell-style alternatives). Nested braces are depth-tracked.
func findExpandableBraceRange(pattern string) (open, close int) {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		depth := 1
		hasComma := false
		for j := i + 1; j < len(pattern); j++ {
			switch pattern[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if hasComma {
						return i, j
					}
					// Single-item or empty braces: skip and keep scanning.
					break
				}
			case ',':
				if depth == 1 {
					hasComma = true
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	return -1, -1
}

func splitBraceAlternatives(inner string) []string {
	if inner == "" {
		return []string{""}
	}
	parts := make([]string, 0, 4)
	start := 0
	depth := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, inner[start:])
	return parts
}

// looksLikeUnsupportedBraceGlob detects common shell brace expansions that
// path.Match / rg --glob will treat as literal characters, producing false
// empty matches (e.g. *.{go,ts} or **/*.{js,ts,tsx}).
func looksLikeUnsupportedBraceGlob(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	open := strings.Index(pattern, "{")
	close := strings.LastIndex(pattern, "}")
	if open < 0 || close <= open {
		return false
	}
	inner := pattern[open+1 : close]
	return strings.Contains(inner, ",")
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
