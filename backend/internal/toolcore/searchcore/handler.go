package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// DefaultGlobLimit is the shared glob page size. It was 500 on the remote
// surface and 100 in the local tool; a single tool cannot have two defaults, and
// 100 is the one callers of this tool have always seen first.
const DefaultGlobLimit = 100

// GlobParameters is the schema both surfaces publish. There is one glob tool,
// so there is one list of parameters it accepts: a field that appears here is
// read by the handler below, and a field the handler reads is declared here.
//
// The descriptions are the ones callers have always seen locally, because the
// local surface is the reference implementation this tool converged on.
func GlobParameters() map[string]interface{} {
	return map[string]interface{}{
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
				"default":     DefaultGlobLimit,
				"maximum":     MaxGlobLimit,
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
}

// Searcher is an optional Runtime extension for surfaces that can delegate the
// traversal to ripgrep. The Handler falls back to the pure-Go walker when the
// extension is absent or declines the request.
type Searcher interface {
	Glob(ctx context.Context, opts GlobOptions) (GlobResult, bool, error)
	Grep(ctx context.Context, opts GrepOptions) (GrepResult, bool, error)
}

// BackendReporter lets a Runtime describe the engine it just used: the command
// a human would run and, when the surface can resolve it, the binary path. The
// handler publishes it so diagnostics read the same on both surfaces; a
// runtime that does not implement it simply contributes no command detail.
type BackendReporter interface {
	SearchBackend(engine string) (command, path string)
}

func NewGlobHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "glob"
	}
	if def.Version == "" {
		def.Version = "1.0.0"
	}
	return &globHandler{def: def}
}

type globHandler struct {
	def toolcore.Definition
}

func (h *globHandler) Definition() toolcore.Definition { return h.def }

func (h *globHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	paths, failure := runtimePaths(call)
	if failure != nil {
		return failure, nil
	}
	pattern := stringArg(call.Args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass pattern", nil), nil
	}
	// A pattern that is absolute or climbs out of the search root can never be
	// satisfied by a root-relative match. Rejecting it beats silently returning
	// nothing (and means no future engine can turn it into an escape).
	if err := validateGlobPatternShape(pattern); err != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass a pattern relative to the search path", err), nil
	}
	base, err := paths.Resolve(stringArg(call.Args, "path"))
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, base); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	// A missing or unreadable search root is a caller-visible failure, not an
	// empty result: "no matches" and "there is nothing there" lead to different
	// next actions. The local tool says which path it could not use, and the
	// remote surface now says the same.
	if _, statErr := os.Stat(base); statErr != nil {
		hint := ""
		if hinter, ok := call.Runtime.(toolcore.PathHinter); ok {
			hint = hinter.PathHint(stringArg(call.Args, "path"))
		}
		message := fmt.Errorf("搜索路径不可用: %s", paths.Relative(base))
		if hint != "" {
			message = fmt.Errorf("搜索路径不可用: %s\n%s", paths.Relative(base), hint)
		}
		if os.IsNotExist(statErr) {
			return toolcore.Error(toolcore.ErrNotFound,
				"check the path; narrow it to a directory that exists in the workspace",
				message), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument,
			"check the path permissions and retry",
			message), nil
	}
	limit := intArg(call.Args, "limit", DefaultGlobLimit)
	if limit < 1 {
		// An explicit limit:0 is a mistake, not a request for the default; the
		// local tool reports it rather than silently returning 100 rows.
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass limit >= 1, or omit it for the default",
			fmt.Errorf("limit 参数必须大于 0")), nil
	}
	if limit > MaxGlobLimit {
		limit = MaxGlobLimit
	}
	offset, offsetOK := intArgOK(call.Args, "offset")
	if offsetOK && offset < 0 {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass offset >= 0",
			fmt.Errorf("offset 参数不能为负数")), nil
	}
	if !offsetOK {
		offset = 0
	}

	engine := "walker"
	caseInsensitive := boolArg(call.Args, "case_insensitive", "ignore_case")
	options := GlobOptions{
		Base:            base,
		Pattern:         pattern,
		Limit:           limit,
		Offset:          offset,
		CaseInsensitive: caseInsensitive,
		// Match order follows the local tool: newest first. It is not exposed as
		// a parameter because both surfaces have always behaved this way.
		SortByModTime: true,
	}
	// One budget covers both engines: the walker and ripgrep must stop at the
	// same deadline, otherwise a slow listing would change behavior based only
	// on whether ripgrep happens to be installed.
	budgetCtx, cancel := context.WithTimeout(ctx, GlobSearchBudget)
	defer cancel()
	result, globErr := GlobWithOptions(budgetCtx, options)
	if searcher, ok := call.Runtime.(Searcher); ok {
		if rgResult, used, rgErr := searcher.Glob(budgetCtx, options); used && rgErr == nil {
			result = rgResult
			globErr = nil
			engine = "ripgrep"
		}
	}
	if globErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return toolcore.Error(toolcore.ErrTimeout, "retry with a narrower path or a longer timeout", ctx.Err()), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the pattern syntax and narrow the base path",
			fmt.Errorf("glob 匹配失败: %w", globErr)), nil
	}

	var builder strings.Builder
	// The byte window is applied while the page is written: the notice is
	// reserved first, so a cut list still ends with a usable next_offset.
	rendered, byteTruncated := renderGlobPage(&builder, paths, result, offset, GlobOutputBudgetBytes)
	body := builder.String()
	if byteTruncated {
		result.Truncated = true
		result.HasMore = true
		result.NextOffset = offset + len(rendered)
	}
	metadata := map[string]any{
		"pattern":          pattern,
		"path":             paths.Relative(base),
		"limit":            limit,
		"offset":           offset,
		"case_insensitive": caseInsensitive,
		"count":            len(rendered),
		"returned_count":   len(rendered),
		"files":            append([]string(nil), rendered...),
		"truncated":        result.Truncated,
		"limit_hit":        result.LimitHit,
		"has_more":         result.HasMore,
		"next_offset":      result.NextOffset,
		"engine":           engine,
		"matches":          len(result.Matches),
		"is_truncated":     result.Truncated,
		"hidden_files":     true,
		"ignore_files":     false,
	}
	command, binaryPath := "", ""
	if reporter, ok := call.Runtime.(BackendReporter); ok {
		command, binaryPath = reporter.SearchBackend(engine)
	}
	metadata["execution_backend"] = engine
	if command != "" {
		metadata["backend_command"] = command
	}
	if binaryPath != "" {
		metadata["backend_path"] = binaryPath
	}
	if byteTruncated {
		metadata["output_window_bytes"] = GlobOutputBudgetBytes
	}
	if result.TimedOut {
		metadata["timed_out"] = true
		metadata["next_action"] = "glob 在 20s 搜索预算内未完成：以上是已收集的部分结果。请收窄 path 或 pattern 后重试，或改用 shell 运行 rg --files 直接列出文件；不要原样重试同一调用。"
		body += globTimeoutNotice()
	}
	if result.BraceExpanded {
		metadata["brace_expanded"] = true
		metadata["expanded_patterns"] = append([]string(nil), result.ExpandedPatterns...)
	}
	if result.UnsupportedBracePattern {
		metadata["unsupported_brace_pattern"] = true
	}
	// A true no-match success carries the empty disposition so the model
	// broadens the query instead of treating it as a hard failure. A page cut
	// by limit or a byte window is not empty: it has a continuation.
	if len(result.Matches) == 0 && !result.Truncated && !result.TimedOut {
		if offset > 0 && result.MatchedTotal > 0 {
			metadata[toolresult.MetadataNextActionKey] = "offset 超出本次抓取的结果窗口：请减小 offset，或收窄 pattern/path 后重新分页（深分页受 5000 条硬上限保护）。"
		} else {
			toolresult.MarkEmptySuccess(metadata)
			if result.UnsupportedBracePattern {
				metadata[toolresult.MetadataNextActionKey] = "glob 无法安全展开该 shell brace pattern（如过大或畸形 *.{go,ts}）。请拆成多次 pattern 调用（*.go、*.ts），或改用 toolkit grep 的 include/glob 数组。不要原样重试同一 brace pattern。"
			}
		}
	}
	// A no-match page still needs the brace guidance in the body: the model
	// reads the body, not the metadata, when deciding how to retry.
	if len(result.Matches) == 0 && result.UnsupportedBracePattern {
		body += "\n（检测到无法安全展开的 shell brace 语法如 *.{a,b}；请拆成多次 pattern 调用，或改用 grep include/glob 数组。）"
	}
	result0 := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    body,
		Metadata:   metadata,
	}
	return shape(call, h.def.Name, result0)
}

func NewGrepHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "grep"
	}
	if def.Version == "" {
		def.Version = "1.0.0"
	}
	return &grepHandler{def: def}
}

type grepHandler struct {
	def toolcore.Definition
}

func (h *grepHandler) Definition() toolcore.Definition { return h.def }

func (h *grepHandler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	paths, failure := runtimePaths(call)
	if failure != nil {
		return failure, nil
	}
	patterns := GrepPatternsFromArgs(call.Args)
	if len(patterns) == 0 {
		// pattern_file(s) can supply every pattern on its own (rg -f).
		if len(GrepGlobListFromArgs(call.Args, "pattern_file")) == 0 &&
			len(GrepGlobListFromArgs(call.Args, "pattern_files")) == 0 {
			return toolcore.Error(toolcore.ErrInvalidArgument, "pass pattern", nil), nil
		}
	}
	roots, failure := grepRootsFromArgs(call, paths)
	if failure != nil {
		return failure, nil
	}
	patternFiles, patternFailure := grepPatternFilesFromArgs(call, paths, roots)
	if patternFailure != nil {
		return patternFailure, nil
	}
	rootLabel := paths.Relative(roots[0])
	mode, modeFailure := grepModeFromArgs(call.Args)
	if modeFailure != nil {
		return modeFailure, nil
	}
	maxCount, maxCountSet := intArgOK(call.Args, "max_count")
	if maxCountSet && maxCount < 0 {
		maxCount = 0
	}
	onlyMatching := boolArg(call.Args, "only_matching")
	// Fragment output and a context window are one page shape, not two:
	// `rg -o -C` prints the matched line's fragments and whole lines around
	// them, which is exactly what ExpandGrepContext now renders.
	// count_matches is "count occurrences instead of lines". In count mode the
	// fragment unit already produces exactly that arithmetic, so it folds into
	// OnlyMatching instead of becoming a separate counting rule. Outside count
	// mode the local tool treats the flag as a no-op, which matches rg: the
	// flag only selects a counting mode.
	countMatches := boolArg(call.Args, "count_matches") && mode == GrepModeCount
	if countMatches {
		onlyMatching = true
	}
	maxColumns, maxColumnsSet := intArgOK(call.Args, "max_columns")
	if maxColumnsSet && maxColumns < 0 {
		maxColumns = 0
	}
	maxColumnsPreview := boolArg(call.Args, "max_columns_preview") &&
		!boolArg(call.Args, "no_max_columns_preview")
	maxDepth, maxDepthSet := intArgOK(call.Args, "max_depth")
	if maxDepthSet && maxDepth < 0 {
		maxDepth = 0
	}
	maxFileSizeBytes, sizeFailure := grepMaxFileSizeFromArgs(call.Args)
	if sizeFailure != nil {
		return sizeFailure, nil
	}
	unrestricted, noIgnore := grepUnrestrictedFromArgs(call.Args)
	// `no_hidden` wins over both `hidden` and the ladder's hidden step,
	// mirroring the local parameter pair.
	includeHidden := !boolArg(call.Args, "no_hidden") &&
		(boolArg(call.Args, "hidden") || unrestricted >= 2)
	ignoreFiles, ignoreFailure := grepIgnoreFilesFromArgs(call, paths, roots)
	if ignoreFailure != nil {
		return ignoreFailure, nil
	}
	contextBefore, contextAfter := grepContextFromArgs(call.Args)
	options := GrepOptions{
		Patterns:                  patterns,
		PatternFiles:              patternFiles,
		Path:                      roots[0],
		Paths:                     roots[1:],
		Mode:                      mode,
		Glob:                      stringArg(call.Args, "glob"),
		Globs:                     GrepGlobListFromArgs(call.Args, "glob"),
		Include:                   GrepGlobListFromArgs(call.Args, "include"),
		Exclude:                   GrepGlobListFromArgs(call.Args, "exclude"),
		Type:                      stringArgAny(call.Args, "type", "file_type"),
		TypeNot:                   stringArg(call.Args, "type_not"),
		CaseInsensitive:           boolArg(call.Args, "case_insensitive"),
		Literal:                   boolArg(call.Args, "literal", "fixed_strings"),
		IgnoreCase:                boolArg(call.Args, "ignore_case"),
		CaseSensitive:             boolArg(call.Args, "case_sensitive"),
		SmartCase:                 boolArg(call.Args, "smart_case"),
		Word:                      boolArg(call.Args, "word", "word_regexp"),
		LineRegexp:                boolArg(call.Args, "line_regexp"),
		InvertMatch:               boolArg(call.Args, "invert_match"),
		MaxCount:                  maxCount,
		MaxCountSet:               maxCountSet,
		OnlyMatching:              onlyMatching,
		CountMatches:              countMatches,
		GlobCaseInsensitive:       boolArg(call.Args, "glob_case_insensitive"),
		IgnoreFileCaseInsensitive: boolArg(call.Args, "ignore_file_case_insensitive"),
		Column:                    boolArg(call.Args, "column"),
		Trim:                      boolArg(call.Args, "trim"),
		MaxColumns:                maxColumns,
		MaxColumnsSet:             maxColumnsSet,
		MaxColumnsPreview:         maxColumnsPreview,
		MaxDepth:                  maxDepth,
		MaxDepthSet:               maxDepthSet,
		MaxFileSizeBytes:          maxFileSizeBytes,
		OneFileSystem:             boolArg(call.Args, "one_file_system"),
		IncludeHidden:             includeHidden,
		Unrestricted:              unrestricted,
		NoIgnore:                  noIgnore,
		NoIgnoreParent:            boolArg(call.Args, "no_ignore_parent"),
		NoIgnoreVCS:               boolArg(call.Args, "no_ignore_vcs"),
		NoIgnoreDot:               boolArg(call.Args, "no_ignore_dot"),
		NoIgnoreGlobal:            boolArg(call.Args, "no_ignore_global"),
		NoIgnoreFiles:             boolArg(call.Args, "no_ignore_files"),
		IgnoreFiles:               ignoreFiles,
		NoMessages:                boolArg(call.Args, "no_messages"),
		Limit:                     intArg(call.Args, "limit", DefaultGrepLimit),
		MaxFileBytes:              captureLimit(call),
		Offset:                    intArg(call.Args, "offset", 0),
		IgnoredPresentation:       collectIgnoredPresentationParams(call.Args),
		Pcre2:                     boolArg(call.Args, "pcre2"),
		Engine:                    stringArg(call.Args, "engine"),
		AutoHybridRegex:           boolArg(call.Args, "auto_hybrid_regex"),
		Multiline:                 boolArg(call.Args, "multiline"),
		MultilineDotall:           boolArg(call.Args, "multiline_dotall"),
		CRLF:                      boolArg(call.Args, "crlf"),
		NoConfig:                  boolArg(call.Args, "no_config"),
		NoConfigSet:               argPresent(call.Args, "no_config"),
		Replace:                   stringArg(call.Args, "replace"),
		JSONOutput:                boolArg(call.Args, "json", "json_output"),
		ContextSeparator:          strings.TrimSpace(stringArg(call.Args, "context_separator")),
		ContextSeparatorSet:       argPresent(call.Args, "context_separator") || boolArg(call.Args, "no_context_separator"),
		NoContextSeparator:        boolArg(call.Args, "no_context_separator"),
	}
	// head_limit is the local spelling of the page size. An explicit 0 means "no
	// page cap" there (up to the safety ceiling), and when both spellings appear
	// it wins: it is the only one that has ever meant "this page's size" on that
	// surface. The ceiling applies to either spelling, so limit and head_limit
	// cannot describe two different maximums.
	headLimit, headLimitSet := intArgOK(call.Args, "head_limit")
	if headLimitSet {
		if headLimit <= 0 {
			options.Limit = GrepUnlimitedPageSize
		} else {
			options.Limit = headLimit
		}
	}
	if options.Limit > GrepUnlimitedPageSize {
		options.Limit = GrepUnlimitedPageSize
	}

	engine := "walker"
	// An explicit ignore_file that cannot be read is a caller-visible failure,
	// not a silent difference in the rule set. NoMessages turns it into a skip.
	if err := ValidateGrepIgnoreFiles(options); err != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument,
			"fix the ignore_file path or pass no_messages to skip unreadable ones", err), nil
	}
	// A request that needs ripgrep is answered by ripgrep first. Go's regexp
	// rejects patterns ripgrep accepts (lookaround, backreferences), so running
	// the walker first would fail a call the engine can serve. When the engine
	// cannot run at all, the fallback is the walker - which is exactly what the
	// builtin tool does when no rg is on PATH.
	var result GrepResult
	var grepErr error
	usedEngine := false
	if options.RequiresRipgrep() {
		if searcher, ok := call.Runtime.(Searcher); ok {
			if rgResult, used, rgErr := searcher.Grep(ctx, options); used && rgErr == nil {
				result, grepErr, engine, usedEngine = rgResult, nil, "ripgrep", true
			}
		}
	}
	if !usedEngine {
		result, grepErr = Grep(ctx, options)
		if !options.RequiresRipgrep() {
			if searcher, ok := call.Runtime.(Searcher); ok {
				if rgResult, used, rgErr := searcher.Grep(ctx, options); used && rgErr == nil {
					result = rgResult
					grepErr = nil
					engine = "ripgrep"
				}
			}
		}
	}
	if grepErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return toolcore.Error(toolcore.ErrTimeout, "retry with a narrower path or a longer timeout", ctx.Err()), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the regex/glob syntax and narrow the path",
			fmt.Errorf("grep %q in %s: %v", strings.Join(patterns, " | "), rootLabel, grepErr)), nil
	}
	// json output is ripgrep's own event format: there is no stream to pass
	// through when the engine could not run, and answering with rendered text
	// under a key that promised JSON Lines is precisely the silent swap this
	// forbids. Say what is missing instead of degrading quietly.
	if options.JSONOutput && engine != "ripgrep" {
		return toolcore.Error(toolcore.ErrInvalidArgument,
			"install ripgrep (rg) to use json/json_output, or drop the flag and read the rendered page",
			fmt.Errorf("grep json output needs the ripgrep engine, which is unavailable on this host")), nil
	}

	renderInputs := grepRenderInputs{
		paths:         paths,
		result:        &result,
		options:       options,
		patterns:      patterns,
		roots:         roots,
		mode:          mode,
		contextBefore: contextBefore,
		contextAfter:  contextAfter,
	}
	page := renderGrepPage(renderInputs)
	if options.JSONOutput {
		// The stream itself is the page: it is sliced by lines and kept
		// parseable, so no paging notice can be appended to it.
		page = renderGrepJSONPage(renderInputs)
	}
	pageMatches := len(result.Matches)
	pageEntries := len(result.Matches)
	switch mode {
	case GrepModeFilesWithout:
		// The page unit is a proven no-match file and there are no matches to
		// count; `files` already carries the page size.
		pageMatches = 0
		pageEntries = len(result.Files)
	case GrepModeContent:
	default:
		pageMatches = 0
		for _, entry := range result.Files {
			pageMatches += entry.Count
		}
		pageEntries = len(result.Files)
	}
	if options.JSONOutput {
		// The page unit is a stream line, and the counters come from ripgrep's
		// own summary events - the same evidence the stream carries.
		pageMatches = result.JSONStats.Matches
		pageEntries = page.renderedLines
	}
	outputFormat := "normalized_text"
	if options.JSONOutput {
		outputFormat = "rg_passthrough"
	}
	includeGlobs, excludeGlobs := EffectiveGrepFilters(options)
	// Effective patterns include every line of the pattern files; report what
	// was actually searched rather than only the direct spellings.
	effectivePatterns := patterns
	if len(options.PatternFiles) > 0 {
		if withFiles, err := GrepPatternsWithFiles(options); err == nil {
			effectivePatterns = withFiles
		}
	}
	metadata := map[string]any{
		"pattern":            strings.Join(effectivePatterns, " | "),
		"patterns":           effectivePatterns,
		"pattern_files":      grepPatternFilesLabels(paths, options.PatternFiles),
		"pattern_count":      len(effectivePatterns),
		"pattern_source":     grepPatternSourceFor(patterns, len(options.PatternFiles) > 0),
		"case_mode":          grepCaseMode(options),
		"mode":               string(mode),
		"path":               rootLabel,
		"paths":              grepRootLabels(paths, roots),
		"search_scope_count": len(roots),
		"search_scope_kind":  grepScopeKind(roots),
		// json output is ripgrep's own stream, passed through: normalization is
		// exactly what did not happen there, and the caller can see which of the
		// two shapes it received.
		"normalized_output":    !options.JSONOutput,
		"match_count":          pageMatches,
		"matches":              pageMatches,
		"files":                len(result.Files),
		"files_scanned":        result.FilesScanned,
		"single_file":          result.SingleFile,
		"is_truncated":         result.Truncated,
		"invert_match":         options.InvertMatch,
		"type":                 options.Type,
		"type_not":             options.TypeNot,
		"max_count":            options.MaxCount,
		"max_count_explicit":   options.MaxCountSet,
		"only_matching":        options.OnlyMatching,
		"column":               options.Column,
		"trim":                 options.Trim,
		"max_columns":          options.MaxColumns,
		"max_columns_explicit": options.MaxColumnsSet,
		"max_columns_preview":  options.MaxColumnsPreview,
		"max_depth":            options.MaxDepth,
		"max_depth_explicit":   options.MaxDepthSet,
		"max_filesize_bytes":   options.MaxFileSizeBytes,
		"one_file_system":      options.OneFileSystem,
		"include_patterns":     len(includeGlobs),
		"exclude_patterns":     len(excludeGlobs),
		"context_before":       contextBefore,
		"context_after":        contextAfter,
		"context_lines":        page.contextLines,
		"offset":               options.Offset,
		"next_offset":          options.Offset + pageEntries,
		"has_more":             result.Truncated,
		"engine":               engine,
		"engine_name":          engine,
		// Published by the handler because the handler is what decides: one of the
		// flags below was set, or a non-default engine was requested.
		"requires_ripgrep": options.RequiresRipgrep(),
		"limit":            options.Limit,
		// One concept, two spellings: the value is the effective page size
		// whichever spelling produced it, and head_limit_explicit says whether
		// the local spelling was the one that did.
		"head_limit":          options.Limit,
		"head_limit_explicit": headLimitSet,
		// The effective separator: "--" when nothing was asked, the caller's
		// string, or empty under no_context_separator - what actually printed.
		"context_separator":    grepContextSeparator(options),
		"no_context_separator": options.NoContextSeparator,
		"hidden_files":         options.IncludeHidden,
		// `ignore_files` is the "is ignore handling on" bit on this surface;
		// the path list lives in ignore_files_list. The local tool uses the
		// same key for the list instead - a long-standing mismatch, left as-is
		// here because changing either side breaks existing readers. The
		// convergence work tracks it rather than silently repurposing a key.
		"ignore_files":      !options.NoIgnore,
		"ignore_files_list": grepIgnoreFilesLabels(paths, options.IgnoreFiles),
		"ignore_file_count": len(options.IgnoreFiles),
		"no_ignore":         options.NoIgnore,
		"no_ignore_parent":  options.NoIgnoreParent,
		"no_ignore_vcs":     options.NoIgnoreVCS,
		"no_ignore_dot":     options.NoIgnoreDot,
		"no_ignore_global":  options.NoIgnoreGlobal,
		"no_ignore_files":   options.NoIgnoreFiles,
		"no_messages":       options.NoMessages,
		// The effective value, not the requested one: unset means the engine told
		// ripgrep not to read its configuration, and publishing the request would
		// describe the opposite of what ran.
		"no_config": !options.NoConfigSet || options.NoConfig,
		// The template the engines substituted with; empty means no replacement
		// was asked for, which is exactly how the call ran.
		"replace":               options.Replace,
		"json":                  options.JSONOutput,
		"output_format":         outputFormat,
		"count_matches":         options.CountMatches,
		"glob_case_insensitive": options.GlobCaseInsensitive,
		// Presentation-only rg flags the caller set are reported, always: the
		// empty list is the statement "your output shape was already the one you
		// asked for", which is information a caller would otherwise have to guess.
		"ignored_presentation": append([]string{}, options.IgnoredPresentation...),
		// Report the flag as off when the list it modifies was discarded:
		// claiming it is on would describe an effect that cannot happen.
		"ignore_file_case_insensitive": options.IgnoreFileCaseInsensitive &&
			!options.NoIgnoreFiles && !options.NoIgnore,
		"literal":            options.Literal,
		"ignore_case":        options.IgnoreCase || options.CaseInsensitive,
		"case_sensitive":     options.CaseSensitive,
		"smart_case":         options.SmartCase,
		"word":               options.Word,
		"line_regexp":        options.LineRegexp,
		"hidden":             options.IncludeHidden,
		"unrestricted_level": options.Unrestricted,
	}
	if options.JSONOutput {
		// The counters ripgrep itself reported travel with the stream; publish
		// them without a separate stats request, exactly like the local tool,
		// and under the names it has always used (ripgrep calls the file
		// counters searches/searches_with_match).
		metadata["stats"] = map[string]any{
			"matches":            result.JSONStats.Matches,
			"matched_lines":      result.JSONStats.MatchedLines,
			"files_with_matches": result.JSONStats.FilesWithMatches,
			"files_searched":     result.JSONStats.FilesSearched,
			"bytes_searched":     result.JSONStats.BytesSearched,
			"bytes_printed":      result.JSONStats.BytesPrinted,
		}
		metadata["json_output_requested"] = true
	} else if boolArg(call.Args, "stats") {
		// Page-level statistics, not a whole-search summary: see GrepStats.
		summary := SummarizeGrepResult(result)
		summary.BytesPrinted = len(page.content)
		if mode == GrepModeFilesWithout {
			// The match counters are unobservable in this mode; publishing
			// zeros would read as "no matches anywhere", which is the exact
			// opposite of what the page is reporting.
			metadata["stats"] = map[string]any{
				"files_without_matches": summary.FilesWithoutMatched,
				"files_scanned":         summary.FilesScanned,
				"files_searched":        summary.FilesScanned,
				"bytes_printed":         summary.BytesPrinted,
			}
		} else {
			metadata["stats"] = map[string]any{
				"matches":            summary.Matches,
				"matched_lines":      summary.MatchedLines,
				"files_with_matched": summary.FilesWithMatched,
				"files_scanned":      summary.FilesScanned,
				"files_searched":     summary.FilesScanned,
				"files_with_matches": summary.FilesWithMatched,
				"bytes_printed":      summary.BytesPrinted,
			}
		}
		metadata["stats_requested"] = true
		page.content += "\n\n" + renderGrepStatsSummary(summary)
	}
	renderGrepMetadataStamps(metadata, page, &result, options.Offset)
	result0 := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    page.content,
		Metadata:   metadata,
	}
	return shape(call, h.def.Name, result0)
}

func runtimePaths(call *toolcore.Call) (toolcore.PathResolver, *toolcore.Result) {
	if call == nil || call.Runtime == nil {
		return nil, toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil)
	}
	paths := call.Runtime.Paths()
	if paths == nil {
		return nil, toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil)
	}
	return paths, nil
}

func shape(call *toolcore.Call, tool string, result *toolcore.Result) (*toolcore.Result, error) {
	if call == nil || call.Runtime == nil || call.Runtime.Output() == nil {
		return result, nil
	}
	shaped, err := call.Runtime.Output().Shape(tool, result)
	if err != nil {
		return toolcore.Error(toolcore.ErrInternal, "retry the call", err), nil
	}
	if shaped == nil {
		return result, nil
	}
	return shaped, nil
}

func captureLimit(call *toolcore.Call) int {
	if call == nil || call.Runtime == nil {
		return 0
	}
	if provider, ok := call.Runtime.(toolcore.CaptureBudgetProvider); ok {
		return provider.CaptureLimit("grep")
	}
	return 0
}

func stringArg(args map[string]any, key string) string {
	if len(args) == 0 {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

// stringArgAny returns the first listed spelling that carries a non-empty
// string, so alias pairs (type/file_type) behave like one parameter.
func stringArgAny(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringArg(args, key)); value != "" {
			return value
		}
	}
	return ""
}

func intArg(args map[string]any, key string, fallback int) int {
	if len(args) == 0 {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return fallback
	}
}

// intArgOK reports an integer argument that the caller actually supplied, so
// "not passed" stays distinguishable from "passed as 0".
func intArgOK(args map[string]any, key string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch value := args[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case float32:
		return int(value), true
	default:
		return 0, false
	}
}

// grepContextFromArgs mirrors the local tool's precedence exactly: `context`
// sets both sides, then before_context/after_context override their own side.
// The shared clamp is applied afterwards.
func grepContextFromArgs(args map[string]any) (int, int) {
	before, after := 0, 0
	if value, ok := intArgOK(args, "context"); ok {
		before, after = value, value
	}
	if value, ok := intArgOK(args, "before_context"); ok {
		before = value
	}
	if value, ok := intArgOK(args, "after_context"); ok {
		after = value
	}
	return ClampGrepContextLines(before), ClampGrepContextLines(after)
}

// grepRootsFromArgs resolves the ordered search roots: `path` first, then every
// entry of `paths`. When neither is supplied the workspace root is searched,
// exactly like the local tool. Every root is admitted before the search starts,
// so one denied path fails the whole call instead of silently returning a
// partial result.
func grepRootsFromArgs(call *toolcore.Call, paths toolcore.PathResolver) ([]string, *toolcore.Result) {
	rawRoots := make([]string, 0, 2)
	if rawPath := strings.TrimSpace(stringArg(call.Args, "path")); rawPath != "" {
		rawRoots = append(rawRoots, rawPath)
	}
	rawRoots = append(rawRoots, GrepPathListFromArgs(call.Args, "paths")...)
	if len(rawRoots) == 0 {
		rawRoots = []string{""}
	}

	roots := make([]string, 0, len(rawRoots))
	for _, raw := range rawRoots {
		resolved, err := paths.Resolve(raw)
		if err != nil {
			return nil, toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
		}
		if permissions := call.Runtime.Permissions(); permissions != nil {
			if err := permissions.CheckPath(toolcore.OpRead, resolved); err != nil {
				return nil, toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
			}
		}
		roots = append(roots, resolved)
	}
	return roots, nil
}

// grepModeFromArgs mirrors the local precedence: the rg-compatible booleans are
// read first, then the explicit `mode` string wins over them.
func grepModeFromArgs(args map[string]any) (GrepMode, *toolcore.Result) {
	raw := ""
	if boolArg(args, "files_with_matches") {
		raw = string(GrepModeFiles)
	}
	if boolArg(args, "files_without_match", "files_without_matches") {
		raw = string(GrepModeFilesWithout)
	}
	if boolArg(args, "count") {
		raw = string(GrepModeCount)
	}
	if boolArg(args, "count_matches") {
		// rg --count-matches is itself a counting mode, so the flag selects
		// count mode on its own. An explicit `mode` still wins below.
		raw = string(GrepModeCount)
	}
	if value, ok := args["mode"].(string); ok && strings.TrimSpace(value) != "" {
		raw = value
	}
	mode, err := NormalizeGrepMode(raw)
	if err != nil {
		return "", toolcore.Error(toolcore.ErrInvalidArgument, "use mode=content|files|files_without|count", err)
	}
	return mode, nil
}

// grepUnrestrictedFromArgs folds the rg-compatible `-u/-uu/-uuu` ladder and its
// structured spellings into one level plus the ignore switch it implies.
//
// The local tool has always treated these as one control: level 0 is the
// default, 1 disables ignore files, 2 also searches hidden paths, 3 also looks
// past a file's first NUL byte. `no_ignore: true` is the same thing as level 1,
// which is how the parameter was documented before the ladder existed.
func grepUnrestrictedFromArgs(args map[string]any) (int, bool) {
	level, set := intArgOK(args, "unrestricted_level")
	if set {
		if level < 0 {
			level = 0
		}
		if level > 3 {
			level = 3
		}
		return level, level > 0
	}
	if boolArg(args, "no_ignore") {
		return 1, true
	}
	return 0, false
}

// grepIgnoreFilesFromArgs resolves the explicit ignore_file(s) spellings into
// absolute paths, so both engines read the same files. The string spelling is
// split on top-level commas exactly like the other scalar lists.
func grepIgnoreFilesFromArgs(call *toolcore.Call, paths toolcore.PathResolver, roots []string) ([]string, *toolcore.Result) {
	raw := append(append([]string{}, GrepGlobListFromArgs(call.Args, "ignore_file")...),
		GrepGlobListFromArgs(call.Args, "ignore_files")...)
	if len(raw) == 0 {
		return nil, nil
	}
	resolved := make([]string, 0, len(raw))
	for _, entry := range raw {
		// A relative ignore_file spelling anchors to the search root's
		// directory (the local tool's workingDir rule), not the process CWD.
		anchor := ""
		if !filepath.IsAbs(entry) && len(roots) > 0 {
			anchor = roots[0]
			if info, statErr := os.Stat(anchor); statErr == nil && !info.IsDir() {
				anchor = filepath.Dir(anchor)
			}
			entry = filepath.Join(anchor, entry)
		}
		abs, err := paths.Resolve(entry)
		if err != nil {
			return nil, toolcore.Error(toolcore.ErrPathDenied, "use an ignore_file inside the workspace", err)
		}
		resolved = append(resolved, abs)
	}
	return resolved, nil
}

// grepPatternFilesFromArgs resolves pattern_file(s) into absolute paths. Both
// spellings are accepted and merged; each file's lines join the pattern list.
func grepPatternFilesFromArgs(call *toolcore.Call, paths toolcore.PathResolver, roots []string) ([]string, *toolcore.Result) {
	raw := append(append([]string{}, GrepGlobListFromArgs(call.Args, "pattern_file")...),
		GrepGlobListFromArgs(call.Args, "pattern_files")...)
	if len(raw) == 0 {
		return nil, nil
	}
	resolved := make([]string, 0, len(raw))
	for _, entry := range raw {
		if !filepath.IsAbs(entry) && len(roots) > 0 {
			anchor := roots[0]
			if info, statErr := os.Stat(anchor); statErr == nil && !info.IsDir() {
				anchor = filepath.Dir(anchor)
			}
			entry = filepath.Join(anchor, entry)
		}
		abs, err := paths.Resolve(entry)
		if err != nil {
			return nil, toolcore.Error(toolcore.ErrPathDenied, "use a pattern_file inside the workspace", err)
		}
		resolved = append(resolved, abs)
	}
	return resolved, nil
}

// grepPageUnit names the pagination unit for the truncation notice.
func grepPageUnit(mode GrepMode) string {
	if mode == GrepModeContent {
		return "matches"
	}
	return "files"
}

// grepPatternSource names where the effective pattern list came from, the same
// way the local tool reported it.
func grepPatternSource(patterns []string) string {
	switch {
	case len(patterns) > 1:
		return "multiple_patterns"
	case len(patterns) == 1:
		return "direct"
	default:
		return "direct"
	}
}

// grepPatternSourceFor folds pattern files into the same vocabulary the local
// tool used (pattern_file / mixed / multiple_patterns / direct).
func grepPatternSourceFor(patterns []string, hasFiles bool) string {
	switch {
	case hasFiles && len(patterns) > 0:
		return "mixed"
	case hasFiles:
		return "pattern_file"
	default:
		return grepPatternSource(patterns)
	}
}

// grepCaseMode reports the effective case strategy, matching the local tool's
// case_mode metadata.
func grepCaseMode(opts GrepOptions) string {
	switch {
	case opts.CaseSensitive:
		return "case_sensitive"
	case opts.IgnoreCase || opts.CaseInsensitive:
		return "ignore_case"
	case opts.SmartCase:
		return "smart_case"
	default:
		return "case_sensitive"
	}
}

// grepPatternFilesLabels renders pattern files as caller-spelled paths.
func grepPatternFilesLabels(paths toolcore.PathResolver, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	labels := make([]string, 0, len(files))
	for _, file := range files {
		labels = append(labels, paths.Relative(file))
	}
	return labels
}

// grepScopeKind names the search-root shape the way the local tool reported
// it, so dashboards keep their vocabulary.
func grepScopeKind(roots []string) string {
	switch {
	case len(roots) > 1:
		return "multi_path"
	case len(roots) == 1:
		return "single"
	default:
		return "single"
	}
}

func grepRootLabels(paths toolcore.PathResolver, roots []string) []string {
	labels := make([]string, 0, len(roots))
	for _, root := range roots {
		labels = append(labels, paths.Relative(root))
	}
	return labels
}

// grepIgnoreFilesLabels renders the explicit ignore files as the caller
// spelled them (relative), mirroring the local metadata shape.
func grepIgnoreFilesLabels(paths toolcore.PathResolver, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	labels := make([]string, 0, len(files))
	for _, file := range files {
		labels = append(labels, paths.Relative(file))
	}
	return labels
}

// collectIgnoredPresentationParams reports which presentation-only rg flags the
// caller set, in the order the builtin tool has always reported them.
//
// These flags are accepted so a model can use the ripgrep spelling it knows, and
// they change nothing: the tool's output is always "path:line:content" with a
// stable skeleton. Reporting them is the honest half - a caller sees that its
// option was understood and deliberately not applied, instead of wondering
// whether it was dropped by accident.
func collectIgnoredPresentationParams(args map[string]any) []string {
	flags := make([]string, 0, 8)
	// Literal reads, not a key table: the schema audit walks real argument reads,
	// and an exemption list would hide exactly what it exists to catch. This is
	// the builtin's own function, moved rather than rewritten.
	if value, ok := args["line_number"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "line_number")
	}
	if value, ok := args["heading"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "heading")
	}
	if value, ok := args["no_heading"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "no_heading")
	}
	if value, ok := args["with_filename"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "with_filename")
	}
	if value, ok := args["no_line_number"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "no_line_number")
	}
	if value, ok := args["no_filename"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "no_filename")
	}
	if value, ok := args["pretty"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "pretty")
	}
	if value, ok := args["line_buffered"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "line_buffered")
	}
	if value, ok := args["no_line_buffered"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "no_line_buffered")
	}
	if value, ok := args["block_buffered"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "block_buffered")
	}
	if value, ok := args["no_block_buffered"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "no_block_buffered")
	}
	if value, ok := args["null"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "null")
	}
	if value, ok := args["null_data"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "null_data")
	}
	if value, ok := args["field_context_separator"].(string); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" && !isQuotedEmptyPlaceholder(trimmed) {
			flags = appendGrepUniqueString(flags, "field_context_separator="+trimmed)
		}
	}
	if value, ok := args["path_separator"].(string); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" && !isQuotedEmptyPlaceholder(trimmed) {
			flags = appendGrepUniqueString(flags, "path_separator="+trimmed)
		}
	}
	if value, ok := args["color"].(string); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" && !isQuotedEmptyPlaceholder(trimmed) {
			flags = appendGrepUniqueString(flags, "color="+trimmed)
		}
	}
	if value, ok := args["text"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "text")
	}
	if value, ok := args["binary"].(bool); ok && value {
		flags = appendGrepUniqueString(flags, "binary")
	}
	return flags
}

// isQuotedEmptyPlaceholder reports the `""` / `”` spelling a model writes to
// mean "the slot is here, leave it alone". The builtin treats it as not passed.
func isQuotedEmptyPlaceholder(value string) bool {
	switch strings.TrimSpace(value) {
	case `""`, `''`:
		return true
	default:
		return false
	}
}

func appendGrepUniqueString(values []string, raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// argPresent reports whether the caller passed the key at all, which is how a
// boolean needs three states: unset, true and false.
func argPresent(args map[string]any, key string) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := args[key]
	return ok
}

// boolArg reports whether any of the listed spellings is set to true.
func boolArg(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := args[key].(bool); ok && value {
			return true
		}
	}
	return false
}

// grepMaxFileSizeFromArgs parses max_filesize in either accepted spelling: an
// integer byte count or a string with a K/M/G suffix. Blank values (including
// the quoted-empty placeholders models sometimes emit) mean "unset", matching
// the local resolver.
func grepMaxFileSizeFromArgs(args map[string]any) (int64, *toolcore.Result) {
	if len(args) == 0 {
		return 0, nil
	}
	raw, exists := args["max_filesize"]
	if !exists || raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || trimmed == `""` || trimmed == "''" {
			return 0, nil
		}
		parsed, err := ParseGrepSize(trimmed)
		if err != nil {
			return 0, toolcore.Error(toolcore.ErrInvalidArgument, "use a byte count or a K/M/G suffix",
				fmt.Errorf("max_filesize 参数无效: %w", err))
		}
		return parsed, nil
	case int:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		return int64(value), nil
	case float32:
		return int64(value), nil
	default:
		return 0, nil
	}
}
