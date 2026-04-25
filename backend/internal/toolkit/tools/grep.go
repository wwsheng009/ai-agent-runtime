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
	"sort"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

var errGrepLimitReached = errors.New("grep match limit reached")

// grepMode defines the output format for search results.
type grepMode string

const (
	grepModeContent grepMode = "content" // show matching lines (default)
	grepModeFiles   grepMode = "files"   // list files with matches only
	grepModeCount   grepMode = "count"   // show match count per file
)

// GrepTool 文件内容搜索工具
type GrepTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxMatches  int
	lookPath    func(string) (string, error)
	runCommand  func(context.Context, string, string, []string) ([]byte, error)
	rgAvailable *bool // cached ripgrep availability (nil = not checked yet)
}

// grepOptions holds all parsed search options.
type grepOptions struct {
	pattern      string
	searchPath   string
	resolvedPath string
	include      string
	exclude      string
	literal      bool
	ignoreCase   bool
	word         bool
	context      int
	fileType     string
	maxDepth     int
	mode         grepMode
}

// fileTypeToGlob maps rg --type names to glob patterns for the builtin engine.
var fileTypeToGlob = map[string]string{
	"go":    "*.go",
	"py":    "*.py",
	"java":  "*.java",
	"js":    "*.js",
	"ts":    "*.ts",
	"tsx":   "*.tsx",
	"jsx":   "*.jsx",
	"rs":    "*.rs",
	"rb":    "*.rb",
	"cpp":   "*.cpp,*.hpp,*.cc,*.cxx",
	"c":     "*.c,*.h",
	"cs":    "*.cs",
	"swift": "*.swift",
	"kt":    "*.kt",
	"scala": "*.scala",
	"php":   "*.php",
	"html":  "*.html,*.htm",
	"css":   "*.css",
	"json":  "*.json",
	"yaml":  "*.yaml,*.yml",
	"toml":  "*.toml",
	"xml":   "*.xml",
	"md":    "*.md,*.markdown",
	"sh":    "*.sh,*.bash",
	"sql":   "*.sql",
	"proto": "*.proto",
	"dart":  "*.dart",
}

// NewGrepTool 创建 Grep 工具
func NewGrepTool() *GrepTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "搜索模式（正则表达式或字面文本，由 literal 参数控制）",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索路径（默认为当前目录）",
			},
			"include": map[string]interface{}{
				"type":        "string",
				"description": "包含的文件名 glob 模式，例如 *.go（默认搜索所有文件）",
			},
			"exclude": map[string]interface{}{
				"type":        "string",
				"description": "排除的文件名 glob 模式，例如 *.test.ts（默认不排除）",
			},
			"literal": map[string]interface{}{
				"type":        "boolean",
				"description": "是否为字面文本搜索，关闭正则特殊字符解释（默认为 false）",
			},
			"ignore_case": map[string]interface{}{
				"type":        "boolean",
				"description": "是否忽略大小写（默认为 false，区分大小写）",
			},
			"word": map[string]interface{}{
				"type":        "boolean",
				"description": "是否匹配完整单词（默认为 false）。例如搜 err 不会匹配 error/stderr",
			},
			"context": map[string]interface{}{
				"type":        "integer",
				"description": "显示匹配行前后各 N 行上下文（默认为 0，不显示上下文）。有助于理解代码结构",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "按文件类型过滤，例如 go/py/java/ts/rs 等（比 include 更语义化）",
			},
			"max_depth": map[string]interface{}{
				"type":        "integer",
				"description": "目录搜索最大深度（默认不限制）",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "输出模式：content=显示匹配行（默认），files=仅列出包含匹配的文件，count=统计每个文件的匹配数",
			},
		},
		"required": []string{"pattern"},
	}

	return &GrepTool{
		BaseTool: toolkit.NewBaseTool(
			"grep",
			"文件内容搜索（优先使用 ripgrep/rg，不可用时回退到内置扫描）",
			"2.0.0",
			parameters,
			true,
		),
		maxMatches: 100,
		lookPath:   exec.LookPath,
		runCommand: runGrepCommand,
	}
}

// Description returns a dynamic description based on ripgrep availability.
func (g *GrepTool) Description() string {
	if g.isRgAvailable() {
		return "文件内容搜索（使用 ripgrep/rg 引擎，高性能正则和字面搜索）。支持上下文行、忽略大小写、整词匹配、文件类型过滤等"
	}
	return "文件内容搜索（使用内置扫描引擎；安装 ripgrep/rg 可获得更好性能）。支持上下文行、忽略大小写、整词匹配等"
}

// isRgAvailable checks if ripgrep is available on the system, caching the result.
func (g *GrepTool) isRgAvailable() bool {
	if g == nil || g.lookPath == nil {
		return false
	}
	if g.rgAvailable != nil {
		return *g.rgAvailable
	}
	available := false
	if rgPath, err := g.lookPath("rg"); err == nil && strings.TrimSpace(rgPath) != "" {
		available = true
	}
	g.rgAvailable = &available
	return available
}

// Execute 实现 Tool 接口
func (g *GrepTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	opts, err := g.parseOptions(params)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if err := g.checkPath(runtimeexecutor.OpRead, opts.resolvedPath); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	// Compile regex for builtin engine (rg engine handles pattern natively)
	re, err := compileGrepPattern(opts.pattern, opts.literal, opts.ignoreCase, opts.word)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if result, used, rgErr := g.searchWithRipgrep(ctx, opts); rgErr != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      rgErr,
		}, nil
	} else if used {
		return result, nil
	}

	return g.searchWithWalker(opts, re), nil
}

// parseOptions extracts and validates all search parameters.
func (g *GrepTool) parseOptions(params map[string]interface{}) (*grepOptions, error) {
	pattern, ok := params["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("pattern 参数缺失或无效")
	}

	searchPath := "."
	if path, ok := params["path"].(string); ok && path != "" {
		searchPath = path
	}

	include := ""
	if inc, ok := params["include"].(string); ok && inc != "" {
		include = inc
	}

	exclude := ""
	if exc, ok := params["exclude"].(string); ok && exc != "" {
		exclude = exc
	}

	literal := resolveLiteralSearchParam(params)
	ignoreCase := resolveBoolParam(params, "ignore_case")
	word := resolveBoolParam(params, "word")
	contextLines := resolveIntParam(params, "context")
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > 10 {
		contextLines = 10 // cap to avoid excessive output
	}

	fileType := ""
	if ft, ok := params["type"].(string); ok && ft != "" {
		fileType = strings.TrimSpace(ft)
	}

	maxDepth := resolveIntParam(params, "max_depth")
	if maxDepth < 0 {
		maxDepth = 0
	}

	mode := grepModeContent
	if m, ok := params["mode"].(string); ok && m != "" {
		switch grepMode(m) {
		case grepModeFiles, grepModeCount:
			mode = grepMode(m)
		default:
			mode = grepModeContent
		}
	}

	return &grepOptions{
		pattern:      pattern,
		searchPath:   searchPath,
		resolvedPath: g.resolvePath(searchPath),
		include:      include,
		exclude:      exclude,
		literal:      literal,
		ignoreCase:   ignoreCase,
		word:         word,
		context:      contextLines,
		fileType:     fileType,
		maxDepth:     maxDepth,
		mode:         mode,
	}, nil
}

func compileGrepPattern(pattern string, literal, ignoreCase, word bool) (*regexp.Regexp, error) {
	expr := pattern
	if literal {
		expr = regexp.QuoteMeta(pattern)
	}
	if word {
		expr = `\b` + expr + `\b`
	}
	if ignoreCase {
		expr = `(?i)` + expr
	}
	re, err := regexp.Compile(expr)
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

func resolveBoolParam(params map[string]interface{}, key string) bool {
	if v, ok := params[key].(bool); ok {
		return v
	}
	return false
}

func resolveIntParam(params map[string]interface{}, key string) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// --- Ripgrep engine ---

func (g *GrepTool) searchWithRipgrep(ctx context.Context, opts *grepOptions) (*toolkit.ToolResult, bool, error) {
	if g == nil || g.lookPath == nil || g.runCommand == nil {
		return nil, false, nil
	}
	rgPath, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(rgPath) == "" {
		return nil, false, nil
	}

	args := buildRipgrepArgs(opts, g.maxMatches)
	output, err := g.runCommand(ctx, rgPath, opts.resolvedPath, args)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		if isRipgrepNoMatch(err) {
			return buildGrepResultWithEngine(opts, nil, 0, false, "rg"), true, nil
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
	return buildGrepResultWithEngine(opts, lines, matchCount, truncated, "rg"), true, nil
}

func buildRipgrepArgs(opts *grepOptions, maxMatches int) []string {
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

	// Context lines
	if opts.context > 0 {
		args = append(args, "--context", fmt.Sprintf("%d", opts.context))
	}

	// Case insensitive
	if opts.ignoreCase {
		args = append(args, "--ignore-case")
	}

	// Word match
	if opts.word {
		args = append(args, "--word-regexp")
	}

	// File type (rg native)
	if opts.fileType != "" {
		args = append(args, "--type", opts.fileType)
	}

	// Max depth
	if opts.maxDepth > 0 {
		args = append(args, "--max-depth", fmt.Sprintf("%d", opts.maxDepth))
	}

	// Include glob
	if opts.include != "" {
		args = append(args, "--glob", filepath.ToSlash(opts.include))
	}

	// Exclude glob
	if opts.exclude != "" {
		args = append(args, "--glob", "!" + filepath.ToSlash(opts.exclude))
	}

	// Mode
	switch opts.mode {
	case grepModeFiles:
		args = append(args, "--files-with-matches")
	case grepModeCount:
		args = append(args, "--count")
	default:
		// content mode: apply max-matches per file
		if maxMatches > 0 {
			args = append(args, "--max-count", fmt.Sprintf("%d", maxMatches))
		}
	}

	// Literal search
	if opts.literal {
		args = append(args, "-F")
	}

	args = append(args, "--", opts.pattern)
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
		// Skip rg context separators (--)
		if line == "--" {
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

// --- Builtin walker engine ---

type grepMatch struct {
	filePath string // relative path
	lineNum  int
	line     string
}

type fileMatchCount struct {
	filePath string
	count    int
}

func (g *GrepTool) searchWithWalker(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	switch opts.mode {
	case grepModeFiles:
		return g.walkerSearchFiles(opts, re)
	case grepModeCount:
		return g.walkerSearchCount(opts, re)
	default:
		return g.walkerSearchContent(opts, re)
	}
}

func (g *GrepTool) walkerSearchContent(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	matches := make([]grepMatch, 0, 16)
	matchCount := 0

	err := g.walkFiles(opts, func(path string, relPath string) error {
		lines, err := readFileLines(path)
		if err != nil {
			return nil
		}

		for i, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, grepMatch{filePath: relPath, lineNum: i + 1, line: line})
			matchCount++
			if matchCount >= g.maxMatches {
				return errGrepLimitReached
			}
		}
		return nil
	})

	if err != nil && !errors.Is(err, errGrepLimitReached) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	// Build output with optional context
	var results []string
	if opts.context > 0 && len(matches) > 0 {
		results = g.buildContextOutput(opts, matches)
	} else {
		results = make([]string, len(matches))
		for i, m := range matches {
			results[i] = fmt.Sprintf("%s:%d: %s", m.filePath, m.lineNum, m.line)
		}
	}

	truncated := errors.Is(err, errGrepLimitReached)
	return buildGrepResult(opts, results, matchCount, truncated)
}

func (g *GrepTool) walkerSearchFiles(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	fileSet := make(map[string]struct{})

	err := g.walkFiles(opts, func(path string, relPath string) error {
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if re.MatchString(scanner.Text()) {
				fileSet[relPath] = struct{}{}
				return nil // found at least one match, no need to scan further
			}
		}
		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	results := make([]string, 0, len(fileSet))
	for f := range fileSet {
		results = append(results, f)
	}
	sort.Strings(results)

	return buildGrepResult(opts, results, len(results), false)
}

func (g *GrepTool) walkerSearchCount(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	counts := make([]fileMatchCount, 0, 16)

	err := g.walkFiles(opts, func(path string, relPath string) error {
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		count := 0
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if re.MatchString(scanner.Text()) {
				count++
			}
		}
		if count > 0 {
			counts = append(counts, fileMatchCount{filePath: relPath, count: count})
		}
		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	results := make([]string, len(counts))
	totalMatches := 0
	for i, fc := range counts {
		results[i] = fmt.Sprintf("%s:%d", fc.filePath, fc.count)
		totalMatches += fc.count
	}

	return buildGrepResult(opts, results, totalMatches, false)
}

// buildContextOutput produces output with context lines around each match.
func (g *GrepTool) buildContextOutput(opts *grepOptions, matches []grepMatch) []string {
	// Group matches by file for context rendering
	type fileMatches struct {
		relPath string
		lines   []string // all file lines
		matches []grepMatch
	}

	fileGroups := make(map[string]*fileMatches)
	fileOrder := make([]string, 0)

	for _, m := range matches {
		fg, ok := fileGroups[m.filePath]
		if !ok {
			lines, err := readFileLines(filepath.Join(opts.resolvedPath, filepath.FromSlash(m.filePath)))
			if err != nil {
				continue
			}
			fg = &fileMatches{relPath: m.filePath, lines: lines}
			fileGroups[m.filePath] = fg
			fileOrder = append(fileOrder, m.filePath)
		}
		fg.matches = append(fg.matches, m)
	}

	results := make([]string, 0, len(matches)*2)
	ctx := opts.context

	for _, fname := range fileOrder {
		fg := fileGroups[fname]
		if len(results) > 0 {
			results = append(results, "--")
		}

		// Collect all line numbers to display (match lines + context)
		showLines := make(map[int]bool)
		for _, m := range fg.matches {
			start := m.lineNum - ctx
			if start < 1 {
				start = 1
			}
			end := m.lineNum + ctx
			if end > len(fg.lines) {
				end = len(fg.lines)
			}
			for i := start; i <= end; i++ {
				showLines[i] = true
			}
		}

		// Render lines in order
		prevLine := 0
		for lineNum := 1; lineNum <= len(fg.lines); lineNum++ {
			if !showLines[lineNum] {
				continue
			}
			if prevLine > 0 && lineNum > prevLine+1 {
				results = append(results, fmt.Sprintf("%s--", fg.relPath))
			}
			prefix := " "
			for _, m := range fg.matches {
				if m.lineNum == lineNum {
					prefix = ">" // marker for match line
					break
				}
			}
			results = append(results, fmt.Sprintf("%s%s%d: %s", fg.relPath, prefix, lineNum, fg.lines[lineNum-1]))
			prevLine = lineNum
		}
	}

	return results
}

// walkFiles walks the directory tree, calling fn for each file that passes filters.
func (g *GrepTool) walkFiles(opts *grepOptions, fn func(path string, relPath string) error) error {
	includeGlobs := resolveIncludeGlobs(opts)
	excludeGlobs := resolveExcludeGlobs(opts)

	walkErr := filepath.Walk(opts.resolvedPath, func(path string, info os.FileInfo, walkErr error) error {
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
			// Check max_depth
			if opts.maxDepth > 0 {
				rel, err := filepath.Rel(opts.resolvedPath, path)
				if err == nil && rel != "." {
					depth := strings.Count(filepath.ToSlash(rel), "/") + 1
					if depth > opts.maxDepth {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}

		// Apply include filter
		if !matchAnyGlob(filepath.Base(path), includeGlobs) {
			return nil
		}

		// Apply exclude filter
		if len(excludeGlobs) > 0 && matchAnyGlob(filepath.Base(path), excludeGlobs) {
			return nil
		}

		relPath, err := filepath.Rel(opts.resolvedPath, path)
		if err != nil {
			relPath = path
		}

		return fn(path, relPath)
	})

	return walkErr
}

// resolveIncludeGlobs builds the include glob list from include and type parameters.
func resolveIncludeGlobs(opts *grepOptions) []string {
	globs := make([]string, 0, 4)

	if opts.fileType != "" {
		if patterns, ok := fileTypeToGlob[opts.fileType]; ok {
			for _, p := range strings.Split(patterns, ",") {
				globs = append(globs, strings.TrimSpace(p))
			}
		} else {
			// Unknown type: treat as glob pattern fallback
			globs = append(globs, "*."+opts.fileType)
		}
	}

	if opts.include != "" {
		globs = append(globs, opts.include)
	}

	return globs
}

// resolveExcludeGlobs builds the exclude glob list.
func resolveExcludeGlobs(opts *grepOptions) []string {
	if opts.exclude == "" {
		return nil
	}
	return strings.Split(opts.exclude, ",")
}

// matchAnyGlob checks if name matches any of the glob patterns.
// If patterns is empty, everything matches.
func matchAnyGlob(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if matched, err := filepath.Match(p, name); err == nil && matched {
			return true
		}
	}
	return false
}

func shouldSkipGrepDir(name string) bool {
	switch strings.TrimSpace(name) {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

// readFileLines reads an entire file into a slice of lines.
func readFileLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// --- Result building ---

func buildGrepResult(opts *grepOptions, results []string, matchCount int, truncated bool) *toolkit.ToolResult {
	output := "未找到匹配的内容"
	if len(results) > 0 {
		output = strings.Join(results, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个匹配)", len(results))
		}
	}

	engine := "builtin"
	if opts != nil {
		// engine will be set to "rg" by the caller if rg was used
	}

	metadata := map[string]interface{}{
		"pattern":     opts.pattern,
		"path":        opts.searchPath,
		"include":     opts.include,
		"exclude":     opts.exclude,
		"literal":     opts.literal,
		"ignore_case": opts.ignoreCase,
		"word":        opts.word,
		"context":     opts.context,
		"type":        opts.fileType,
		"max_depth":   opts.maxDepth,
		"mode":        string(opts.mode),
		"match_count": matchCount,
		"truncated":   truncated,
		"engine":      engine,
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata:   metadata,
	}
}

func buildGrepResultWithEngine(opts *grepOptions, results []string, matchCount int, truncated bool, engine string) *toolkit.ToolResult {
	result := buildGrepResult(opts, results, matchCount, truncated)
	result.Metadata["engine"] = engine
	return result
}
