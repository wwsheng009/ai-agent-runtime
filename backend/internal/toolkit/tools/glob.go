package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimeripgrep "github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	defaultGlobLimit = 100
	maxGlobLimit     = 1000
	// globFetchHardCap 限制「offset+limit」一次抓取的候选量：分页是给模型
	// 翻页用的，不应该变成把整棵目录树读进内存的借口。
	globFetchHardCap = 5000
	// globSearchBudget 是单次 glob 的搜索预算：超时返回已收集结果
	// （timed_out=true），而不是让整次调用失败。
	globSearchBudget = 20 * time.Second
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
				"description": "最多返回的匹配数量（默认为 100，最大 1000）。结果按 mtime 从新到旧排序，可用 offset/limit 分页。",
				"default":     defaultGlobLimit,
				"maximum":     maxGlobLimit,
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "跳过按 mtime 排序后的前 N 个匹配（默认 0），配合 limit 翻页；返回的 next_offset 可直接用于下一页。",
				"default":     0,
				"minimum":     0,
			},
		},
		"required": []string{"pattern"},
	}

	return &GlobTool{
		BaseTool: toolkit.NewBaseTool(
			"glob",
			"文件名/路径模式匹配搜索，不搜索文件内容。支持 * ? [] **；常见 shell brace（*.{go,ts}）会自动展开。结果按 mtime 从新到旧排序，可用 offset/limit 分页（next_offset 续读）。递归文件匹配优先用 rg --files；目录匹配、单层匹配或 rg 不可用时回退内置遍历；20s 预算内未完成会返回已收集的部分结果（timed_out=true）。大小写不确定时用 case_insensitive=true。",
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
	resolvedSearchPath := g.resolvePathWithContext(ctx, searchPath)
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
				Error:      g.buildPathNotFoundError(ctx, "搜索路径不可用", searchPath),
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
	offset := 0
	if rawOffset, ok := params["offset"]; ok && rawOffset != nil {
		parsedOffset, err := parseGlobOffset(rawOffset)
		if err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
		offset = parsedOffset
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

	// 抓取 offset+limit 个候选（硬上限 globFetchHardCap）：排序后分页，早停
	// 只会丢「更旧」的匹配，不会让下一页缺项。
	fetchLimit := limit
	if offset > 0 {
		fetchLimit = offset + limit
	}
	fetchClipped := false
	if fetchLimit > globFetchHardCap {
		fetchLimit = globFetchHardCap
		fetchClipped = offset+limit > globFetchHardCap
	}

	// 搜索预算：超时返回已收集结果（timed_out=true），而不是整体失败。
	searchCtx, cancelSearch := context.WithTimeout(ctx, globSearchBudget)
	defer cancelSearch()
	matches, truncated, timedOut, engine, err := g.findMatchesMulti(searchCtx, resolvedSearchPath, expandedPatterns, searchPathInfo.IsDir(), fetchLimit, caseInsensitive)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("glob 匹配失败: %w", err),
		}, nil
	}
	// mtime 从新到旧（同 mtime 按路径）→ offset/limit 分页。
	ordered := sortGlobMatchesByModTime(resolvedSearchPath, matches)
	pageStart := offset
	if pageStart > len(ordered) {
		pageStart = len(ordered)
	}
	page := ordered[pageStart:]
	if len(page) > limit {
		page = page[:limit]
		truncated = true
	}
	if fetchClipped {
		truncated = true
	}
	hasMore := truncated || len(ordered) > pageStart+len(page)

	// 格式化输出。结果集同时受 limit 与 glob 自身字节预算约束：预算内截断
	// 同样标记 truncated，绝不把已定形的列表交给 L4 二次折叠。
	var output string
	braceHint := ""
	if braceUnsupported {
		braceHint = "（检测到无法安全展开的 shell brace 语法如 *.{a,b}；请拆成多次 pattern 调用，或改用 grep include/glob 数组。）"
	}
	rendered := page
	if len(page) == 0 {
		output = "未找到匹配项" + braceHint
		if hasMore {
			output += globPaginationNotice(offset, offset, offset)
		}
	} else {
		// 截断提示与文件列表同属 glob 的字节预算：先按最坏情况预留提示长度，
		// 列表只在剩余额度内写入，payload 才真正不超过 globOutputBudgetBytes。
		// 截断提示与分页提示不会同时出现（见下方 emission），预留取两者较大值：
		// 既不超预算，也不无条件多扣一行列表容量。
		noticeReserve := len(globTruncationNotice(pageStart+len(page), offset+len(page)))
		if paginationReserve := len(globPaginationNotice(offset, offset+len(page), offset+len(page))); paginationReserve > noticeReserve {
			noticeReserve = paginationReserve
		}
		listingBudget := globOutputBudgetBytes - noticeReserve
		if listingBudget < 0 {
			listingBudget = 0
		}
		used := 0
		kept := 0
		for _, match := range page {
			lineBytes := len(match) + 1
			if kept > 0 && used+lineBytes > listingBudget {
				break
			}
			used += lineBytes
			kept++
		}
		if kept < len(page) {
			rendered = page[:kept]
			truncated = true
		}
		output = strings.Join(rendered, "\n")
		if truncated {
			// 默认 limit/字节预算截断同样要给出续读入口：只报“显示前 N 个文件”
			// 会让模型把窗口读成数据丢失，然后换词重搜而不是分页。
			output += globTruncationNotice(pageStart+len(rendered), offset+len(rendered))
		} else if hasMore {
			nextOffset := offset + len(rendered)
			output += globPaginationNotice(offset, nextOffset, nextOffset)
		}
	}
	if timedOut {
		output += globTimeoutNotice()
	}

	metadata := map[string]interface{}{
		"pattern":          pattern,
		"path":             searchPath,
		"limit":            limit,
		"offset":           offset,
		"case_insensitive": caseInsensitive,
		"count":            len(rendered), // 兼容字段：返回数量
		"returned_count":   len(rendered),
		"files":            append([]string(nil), rendered...),
		"truncated":        truncated, // 兼容字段：是否被截断
		"limit_hit":        truncated,
		"has_more":         hasMore,
		"next_offset":      offset + len(rendered),
		"engine":           engine,
	}
	if timedOut {
		metadata["timed_out"] = true
		metadata[toolresult.MetadataNextActionKey] = "glob 在 20s 搜索预算内未完成：以上是已收集的部分结果。请收窄 path 或 pattern 后重试，或改用 shell 运行 rg --files 直接列出文件；不要原样重试同一调用。"
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
	if len(page) == 0 && !truncated && !timedOut {
		if offset > 0 && len(ordered) > 0 {
			metadata[toolresult.MetadataNextActionKey] = "offset 超出本次抓取的结果窗口：请减小 offset，或收窄 pattern/path 后重新分页（深分页受 5000 条硬上限保护）。"
		} else {
			toolresult.MarkEmptySuccess(metadata)
			if braceUnsupported {
				metadata[toolresult.MetadataNextActionKey] = "glob 无法安全展开该 shell brace pattern（如过大或畸形 *.{go,ts}）。请拆成多次 pattern 调用（*.go、*.ts），或改用 toolkit grep 的 include/glob 数组。不要原样重试同一 brace pattern。"
			}
		}
	}

	return stampToolOwnsOutputWithBudget(&toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata:   metadata,
	}, globOutputBudgetBytes), nil
}

// globTruncationNotice renders the notice glob appends after a truncated file
// list. It carries the continuation offset so the default (non-paginated) path
// is as actionable as an explicit offset call: without it the model reads the
// window as data loss and re-runs the same search with different wording.
// The entry count only grows with its digit count, so the page size is a valid
// byte ceiling when reserving room for the notice inside the glob window.
func globTruncationNotice(shown, next int) string {
	return fmt.Sprintf("\n\n(已分页显示前 %d 个文件；next_offset=%d 可继续分页)", shown, next)
}

// globPaginationNotice 告诉模型当前页在排序后结果中的位置与下一页 offset。
func globPaginationNotice(start, end, next int) string {
	return fmt.Sprintf("\n(显示第 %d-%d 个文件；next_offset=%d 可继续分页)", start+1, end, next)
}

// globTimeoutNotice 标记「预算耗尽、结果是部分结果」，避免模型把它当成完整列表。
func globTimeoutNotice() string {
	return "\n(搜索预算 20s 内未完成：以上是已收集的部分结果；请收窄 path/pattern 后重试，或改用 shell 运行 rg --files)"
}

// parseGlobOffset 解析 offset：非负整数，0 表示不跳过。
func parseGlobOffset(raw interface{}) (int, error) {
	var offset int64
	switch v := raw.(type) {
	case int:
		offset = int64(v)
	case int8:
		offset = int64(v)
	case int16:
		offset = int64(v)
	case int32:
		offset = int64(v)
	case int64:
		offset = v
	case float32:
		offset = int64(v)
	case float64:
		offset = int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("offset 参数无效")
		}
		offset = parsed
	default:
		return 0, fmt.Errorf("offset 参数无效")
	}
	if offset < 0 {
		return 0, fmt.Errorf("offset 参数不能为负数")
	}
	if offset > globFetchHardCap {
		offset = globFetchHardCap
	}
	return int(offset), nil
}

// sortGlobMatchesByModTime 按 mtime 从新到旧排序（同 mtime 按路径升序）。
// 匹配项是相对 resolvedSearchPath 的路径；stat 失败按零值时间处理（排在最后）。
func sortGlobMatchesByModTime(root string, matches []string) []string {
	if len(matches) == 0 {
		return nil
	}
	type globModEntry struct {
		rel string
		mod time.Time
	}
	entries := make([]globModEntry, len(matches))
	for index, match := range matches {
		mod := time.Time{}
		if info, err := os.Stat(filepath.Join(root, match)); err == nil {
			mod = info.ModTime()
		}
		entries[index] = globModEntry{rel: match, mod: mod}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].mod.Equal(entries[j].mod) {
			return entries[i].mod.After(entries[j].mod)
		}
		return entries[i].rel < entries[j].rel
	})
	ordered := make([]string, len(entries))
	for index := range entries {
		ordered[index] = entries[index].rel
	}
	return ordered
}

// findMatchesMulti unions results across expanded brace patterns while
// respecting the shared limit and de-duplicating paths.
func (g *GlobTool) findMatchesMulti(ctx context.Context, resolvedSearchPath string, patterns []string, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, bool, string, error) {
	if len(patterns) == 0 {
		return nil, false, false, "builtin", nil
	}
	if len(patterns) == 1 {
		return g.findMatches(ctx, resolvedSearchPath, patterns[0], rootIsDir, limit, caseInsensitive)
	}

	matches := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	engine := "builtin"
	timedOut := false
	for _, pattern := range patterns {
		// Request one extra across every alternative so an exact limit from an
		// early pattern does not falsely imply truncation when later patterns are empty.
		requestLimit := 0
		if limit > 0 {
			requestLimit = limit + 1
		}
		part, partTruncated, partTimedOut, partEngine, err := g.findMatches(ctx, resolvedSearchPath, pattern, rootIsDir, requestLimit, caseInsensitive)
		if err != nil {
			return nil, false, false, partEngine, err
		}
		if partTimedOut {
			timedOut = true
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
				return matches[:limit], true, timedOut, engine, nil
			}
		}
		if partTruncated {
			// The child search omitted results. Ordinarily requestLimit guarantees
			// enough returned rows to hit the shared limit; keep the flag defensive.
			if limit > 0 && len(matches) >= limit {
				return matches[:limit], true, timedOut, engine, nil
			}
			return matches, true, timedOut, engine, nil
		}
		if timedOut {
			// 预算已耗尽：不再尝试其余 brace 变体，返回并集的部分结果。
			return matches, false, true, engine, nil
		}
	}
	return matches, false, timedOut, engine, nil
}

func (g *GlobTool) findMatches(ctx context.Context, resolvedSearchPath, pattern string, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, bool, string, error) {
	compiled := compileGlobPattern(pattern)
	if compiled.normalized == "" {
		return nil, false, false, "builtin", nil
	}
	if matches, handled, err := g.findExactMatches(resolvedSearchPath, compiled, rootIsDir, caseInsensitive); handled || err != nil {
		return matches, false, false, "builtin", err
	}
	if matches, truncated, timedOut, used, err := g.findMatchesWithRipgrep(ctx, resolvedSearchPath, compiled, rootIsDir, limit, caseInsensitive); err != nil {
		return nil, false, false, "rg", err
	} else if used {
		return matches, truncated, timedOut, "rg", nil
	}
	if rootIsDir && len(compiled.parts) == 1 && compiled.parts[0] != "**" {
		matches, truncated, timedOut, err := g.findMatchesInCurrentDir(ctx, resolvedSearchPath, compiled, limit, caseInsensitive)
		return matches, truncated, timedOut, "builtin", err
	}
	walkRoot := resolvedSearchPath
	walkPrefixParts := make([]string, 0, len(compiled.parts))
	if rootIsDir && !caseInsensitive {
		if prefix := compiled.staticPrefix; prefix != "" {
			candidateRoot := filepath.Join(resolvedSearchPath, filepath.FromSlash(prefix))
			if _, err := os.Stat(candidateRoot); err != nil {
				if os.IsNotExist(err) {
					return nil, false, false, "builtin", nil
				}
				return nil, false, false, "builtin", err
			}
			walkRoot = candidateRoot
			walkPrefixParts = splitGlobSegments(prefix)
		}
	}
	matches := make([]string, 0, 16)
	truncated, timedOut, err := g.walkGlobTree(ctx, walkRoot, walkPrefixParts, compiled, &matches, limit, caseInsensitive)
	if err != nil {
		return nil, false, false, "builtin", err
	}
	return matches, truncated, timedOut, "builtin", nil
}

func (g *GlobTool) findMatchesWithRipgrep(ctx context.Context, resolvedSearchPath string, compiled compiledGlobPattern, rootIsDir bool, limit int, caseInsensitive bool) ([]string, bool, bool, bool, error) {
	if !shouldUseRipgrepGlob(rootIsDir, compiled) {
		return nil, false, false, false, nil
	}
	if g == nil || g.lookPath == nil || g.runCommand == nil {
		return nil, false, false, false, nil
	}
	rgPath, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(rgPath) == "" {
		return nil, false, false, false, nil
	}

	globFlag := "--glob"
	if caseInsensitive {
		globFlag = "--iglob"
	}
	args := []string{"--files", "--hidden", "--no-ignore", globFlag, compiled.normalized}
	output, err := g.runCommand(ctx, rgPath, resolvedSearchPath, args)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// 预算/取消：已产出的 stdout 当作部分结果返回（truncated+timedOut），
			// 完全没有产出时才如实返回错误。
			if len(strings.TrimSpace(string(output))) > 0 {
				matches, _ := collectRipgrepGlobMatches(output, compiled, limit, caseInsensitive)
				return matches, true, true, true, nil
			}
			return nil, false, false, true, ctxErr
		}
		if isRipgrepNoMatch(err) {
			return nil, false, false, true, nil
		}
		return nil, false, false, false, nil
	}

	matches, truncated := collectRipgrepGlobMatches(output, compiled, limit, caseInsensitive)
	return matches, truncated, false, true, nil
}

// collectRipgrepGlobMatches 解析 rg --files 输出并套用 glob 匹配与上限。
// 部分输出可能截断在半个路径/半个 UTF-8 字符上：这里跳过无法解析的行，
// 而不是让整次搜索失败（预算耗尽的场景由调用方标记 timed_out）。
func collectRipgrepGlobMatches(output []byte, compiled compiledGlobPattern, limit int, caseInsensitive bool) ([]string, bool) {
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
			continue
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
	return matches, truncated
}

func (g *GlobTool) walkGlobTree(ctx context.Context, absDir string, relParts []string, compiled compiledGlobPattern, matches *[]string, limit int, caseInsensitive bool) (bool, bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return len(*matches) > 0, true, nil
		}
	}
	if limit > 0 && len(*matches) >= limit {
		return true, false, nil
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return false, false, err
	}
	for _, entry := range entries {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return true, true, nil
			}
		}
		if limit > 0 && len(*matches) >= limit {
			return true, false, nil
		}
		name := entry.Name()
		relParts = append(relParts, name)
		matched, err := compiled.matchCandidate(relParts, caseInsensitive)
		if err != nil {
			relParts = relParts[:len(relParts)-1]
			return false, false, err
		}
		canDescend := entry.IsDir() && canDescendGlobParts(compiled, relParts, caseInsensitive)
		if matched {
			*matches = append(*matches, filepath.Join(relParts...))
			if limit > 0 && len(*matches) >= limit {
				relParts = relParts[:len(relParts)-1]
				return true, false, nil
			}
		}
		if entry.IsDir() && canDescend {
			nextDir := filepath.Join(absDir, name)
			truncated, timedOut, err := g.walkGlobTree(ctx, nextDir, relParts, compiled, matches, limit, caseInsensitive)
			relParts = relParts[:len(relParts)-1]
			if err != nil {
				return false, false, err
			}
			if timedOut {
				return true, true, nil
			}
			if truncated {
				return true, false, nil
			}
			continue
		}
		relParts = relParts[:len(relParts)-1]
	}
	return false, false, nil
}

func (c compiledGlobPattern) matchCandidate(pathParts []string, caseInsensitive bool) (bool, error) {
	if c.leadingDoubleStar {
		return matchLeadingDoubleStarTail(c.recursiveTail, pathParts, caseInsensitive)
	}
	return matchGlobSegmentsWithCase(c.parts, pathParts, caseInsensitive)
}

func (g *GlobTool) findMatchesInCurrentDir(ctx context.Context, resolvedSearchPath string, compiled compiledGlobPattern, limit int, caseInsensitive bool) ([]string, bool, bool, error) {
	entries, err := os.ReadDir(resolvedSearchPath)
	if err != nil {
		return nil, false, false, err
	}
	matches := make([]string, 0, 16)
	for _, entry := range entries {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return matches, true, true, nil
			}
		}
		matched, err := matchGlobPart(compiled.parts[0], entry.Name(), caseInsensitive)
		if err != nil {
			return nil, false, false, err
		}
		if matched {
			matches = append(matches, entry.Name())
			if limit > 0 && len(matches) >= limit {
				return matches, true, false, nil
			}
		}
	}
	return matches, false, false, nil
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
