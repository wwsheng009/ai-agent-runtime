package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// DefaultGlobLimit mirrors the long-standing remote glob default.
const DefaultGlobLimit = 500

// Searcher is an optional Runtime extension for surfaces that can delegate the
// traversal to ripgrep. The Handler falls back to the pure-Go walker when the
// extension is absent or declines the request.
type Searcher interface {
	Glob(ctx context.Context, opts GlobOptions) (GlobResult, bool, error)
	Grep(ctx context.Context, opts GrepOptions) (GrepResult, bool, error)
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
	base, err := paths.Resolve(stringArg(call.Args, "path"))
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, base); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	limit := intArg(call.Args, "limit", DefaultGlobLimit)

	engine := "walker"
	offset := intArg(call.Args, "offset", 0)
	result, globErr := GlobWithOptions(ctx, GlobOptions{
		Base:    base,
		Pattern: pattern,
		Limit:   limit,
		Offset:  offset,
	})
	if searcher, ok := call.Runtime.(Searcher); ok {
		if rgResult, used, rgErr := searcher.Glob(ctx, GlobOptions{
			Base:    base,
			Pattern: pattern,
			Limit:   limit,
			Offset:  offset,
		}); used && rgErr == nil {
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
			fmt.Errorf("glob %q in %s: %v", pattern, paths.Relative(base), globErr)), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== glob %q in %s ===\n", pattern, paths.Relative(base))
	for _, match := range result.Matches {
		builder.WriteString(paths.Relative(match))
		builder.WriteByte('\n')
	}
	if len(result.Matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if result.Truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern]\n", limit)
	}
	metadata := map[string]any{
		"pattern":      pattern,
		"path":         paths.Relative(base),
		"matches":      len(result.Matches),
		"is_truncated": result.Truncated,
		"offset":       offset,
		"next_offset":  result.NextOffset,
		"has_more":     result.HasMore,
		"engine":       engine,
		"hidden_files": true,
		"ignore_files": false,
	}
	result0 := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    builder.String(),
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
	if onlyMatching && (contextBefore > 0 || contextAfter > 0) {
		// Reporting the conflict beats silently dropping one of the two
		// parameters: the fragment output unit and the line-window expansion
		// cannot both own the same line.
		return toolcore.Error(toolcore.ErrInvalidArgument,
			"drop context or only_matching: fragment output and context windows are not combined by the shared core yet",
			fmt.Errorf("only_matching 与 context 不能同时使用")), nil
	}
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
	}

	engine := "walker"
	// An explicit ignore_file that cannot be read is a caller-visible failure,
	// not a silent difference in the rule set. NoMessages turns it into a skip.
	if err := ValidateGrepIgnoreFiles(options); err != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument,
			"fix the ignore_file path or pass no_messages to skip unreadable ones", err), nil
	}
	result, grepErr := Grep(ctx, options)
	if searcher, ok := call.Runtime.(Searcher); ok {
		if rgResult, used, rgErr := searcher.Grep(ctx, options); used && rgErr == nil {
			result = rgResult
			grepErr = nil
			engine = "ripgrep"
		}
	}
	if grepErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return toolcore.Error(toolcore.ErrTimeout, "retry with a narrower path or a longer timeout", ctx.Err()), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the regex/glob syntax and narrow the path",
			fmt.Errorf("grep %q in %s: %v", strings.Join(patterns, " | "), rootLabel, grepErr)), nil
	}

	page := renderGrepPage(grepRenderInputs{
		paths:         paths,
		result:        &result,
		options:       options,
		patterns:      patterns,
		roots:         roots,
		mode:          mode,
		contextBefore: contextBefore,
		contextAfter:  contextAfter,
	})
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
		"pattern":              strings.Join(effectivePatterns, " | "),
		"patterns":             effectivePatterns,
		"pattern_files":        grepPatternFilesLabels(paths, options.PatternFiles),
		"pattern_count":        len(effectivePatterns),
		"pattern_source":       grepPatternSourceFor(patterns, len(options.PatternFiles) > 0),
		"case_mode":            grepCaseMode(options),
		"mode":                 string(mode),
		"path":                 rootLabel,
		"paths":                grepRootLabels(paths, roots),
		"search_scope_count":   len(roots),
		"search_scope_kind":    grepScopeKind(roots),
		"normalized_output":    true,
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
		"limit":                options.Limit,
		"hidden_files":         options.IncludeHidden,
		// `ignore_files` is the "is ignore handling on" bit on this surface;
		// the path list lives in ignore_files_list. The local tool uses the
		// same key for the list instead - a long-standing mismatch, left as-is
		// here because changing either side breaks existing readers. The
		// convergence work tracks it rather than silently repurposing a key.
		"ignore_files":          !options.NoIgnore,
		"ignore_files_list":     grepIgnoreFilesLabels(paths, options.IgnoreFiles),
		"ignore_file_count":     len(options.IgnoreFiles),
		"no_ignore":             options.NoIgnore,
		"no_ignore_parent":      options.NoIgnoreParent,
		"no_ignore_vcs":         options.NoIgnoreVCS,
		"no_ignore_dot":         options.NoIgnoreDot,
		"no_ignore_global":      options.NoIgnoreGlobal,
		"no_ignore_files":       options.NoIgnoreFiles,
		"no_messages":           options.NoMessages,
		"count_matches":         options.CountMatches,
		"glob_case_insensitive": options.GlobCaseInsensitive,
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
	if boolArg(call.Args, "stats") {
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
