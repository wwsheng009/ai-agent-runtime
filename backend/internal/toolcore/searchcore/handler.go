package searchcore

import (
	"context"
	"fmt"
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
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass pattern", nil), nil
	}
	roots, failure := grepRootsFromArgs(call, paths)
	if failure != nil {
		return failure, nil
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
	maxDepth, maxDepthSet := intArgOK(call.Args, "max_depth")
	if maxDepthSet && maxDepth < 0 {
		maxDepth = 0
	}
	maxFileSizeBytes, sizeFailure := grepMaxFileSizeFromArgs(call.Args)
	if sizeFailure != nil {
		return sizeFailure, nil
	}
	// `no_hidden` wins over `hidden`, mirroring the local parameter pair.
	includeHidden := boolArg(call.Args, "hidden") && !boolArg(call.Args, "no_hidden")
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
		Patterns:         patterns,
		Path:             roots[0],
		Paths:            roots[1:],
		Mode:             mode,
		Glob:             stringArg(call.Args, "glob"),
		Include:          GrepGlobListFromArgs(call.Args, "include"),
		Exclude:          GrepGlobListFromArgs(call.Args, "exclude"),
		Type:             stringArgAny(call.Args, "type", "file_type"),
		TypeNot:          stringArg(call.Args, "type_not"),
		CaseInsensitive:  boolArg(call.Args, "case_insensitive"),
		Literal:          boolArg(call.Args, "literal", "fixed_strings"),
		IgnoreCase:       boolArg(call.Args, "ignore_case"),
		CaseSensitive:    boolArg(call.Args, "case_sensitive"),
		SmartCase:        boolArg(call.Args, "smart_case"),
		Word:             boolArg(call.Args, "word", "word_regexp"),
		LineRegexp:       boolArg(call.Args, "line_regexp"),
		InvertMatch:      boolArg(call.Args, "invert_match"),
		MaxCount:         maxCount,
		MaxCountSet:      maxCountSet,
		OnlyMatching:     onlyMatching,
		MaxDepth:         maxDepth,
		MaxDepthSet:      maxDepthSet,
		MaxFileSizeBytes: maxFileSizeBytes,
		IncludeHidden:    includeHidden,
		Limit:            intArg(call.Args, "limit", DefaultGrepLimit),
		MaxFileBytes:     captureLimit(call),
		Offset:           intArg(call.Args, "offset", 0),
	}

	engine := "walker"
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

	patternLabel := strings.Join(patterns, " | ")
	contextLineCount := 0
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== grep %q ===\n", patternLabel)
	switch {
	case mode == GrepModeFiles:
		for _, entry := range result.Files {
			builder.WriteString(paths.Relative(entry.File))
			builder.WriteByte('\n')
		}
	case mode == GrepModeCount:
		for _, entry := range result.Files {
			fmt.Fprintf(&builder, "%s:%d\n", paths.Relative(entry.File), entry.Count)
		}
	case contextBefore > 0 || contextAfter > 0:
		// Context is produced by the shared expander, not by an engine flag:
		// match lines keep the `path:line:text` shape, neighbouring lines use
		// ripgrep's `path-line-text` shape, and blocks are separated by `--`.
		expanded := ExpandGrepContext(result.Matches, contextBefore, contextAfter)
		contextLineCount = len(expanded)
		printedAny := false
		for _, line := range expanded {
			if line.GroupStart && printedAny {
				builder.WriteString("--\n")
			}
			relative := paths.Relative(line.File)
			if line.IsMatch {
				fmt.Fprintf(&builder, "%s:%d:%s\n", relative, line.Line, line.Text)
			} else {
				fmt.Fprintf(&builder, "%s-%d-%s\n", relative, line.Line, line.Text)
			}
			printedAny = true
		}
	default:
		for _, item := range result.Matches {
			if item.Column > 0 {
				fmt.Fprintf(&builder, "%s:%d:%d:%s\n", paths.Relative(item.File), item.Line, item.Column, item.Text)
				continue
			}
			fmt.Fprintf(&builder, "%s:%d:%s\n", paths.Relative(item.File), item.Line, item.Text)
		}
	}
	if len(result.Matches) == 0 && len(result.Files) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if result.Truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d %s; narrow the pattern or glob]\n", options.Limit, grepPageUnit(mode))
	}
	pageMatches := len(result.Matches)
	pageEntries := len(result.Matches)
	if mode != GrepModeContent {
		pageMatches = 0
		for _, entry := range result.Files {
			pageMatches += entry.Count
		}
		pageEntries = len(result.Files)
	}
	includeGlobs, excludeGlobs := EffectiveGrepFilters(options)
	metadata := map[string]any{
		"pattern":            patternLabel,
		"patterns":           patterns,
		"mode":               string(mode),
		"path":               rootLabel,
		"paths":              grepRootLabels(paths, roots),
		"matches":            pageMatches,
		"files":              len(result.Files),
		"files_scanned":      result.FilesScanned,
		"single_file":        result.SingleFile,
		"is_truncated":       result.Truncated,
		"invert_match":       options.InvertMatch,
		"type":               options.Type,
		"type_not":           options.TypeNot,
		"max_count":          options.MaxCount,
		"max_count_explicit": options.MaxCountSet,
		"only_matching":      options.OnlyMatching,
		"max_depth":          options.MaxDepth,
		"max_depth_explicit": options.MaxDepthSet,
		"max_filesize_bytes": options.MaxFileSizeBytes,
		"include_patterns":   len(includeGlobs),
		"exclude_patterns":   len(excludeGlobs),
		"context_before":     contextBefore,
		"context_after":      contextAfter,
		"context_lines":      contextLineCount,
		"offset":             options.Offset,
		"next_offset":        options.Offset + pageEntries,
		"has_more":           result.Truncated,
		"engine":             engine,
		"hidden_files":       options.IncludeHidden,
		"ignore_files":       false,
	}
	if boolArg(call.Args, "stats") {
		// Page-level statistics, not a whole-search summary: see GrepStats.
		summary := SummarizeGrepResult(result)
		metadata["stats"] = map[string]any{
			"matches":            summary.Matches,
			"matched_lines":      summary.MatchedLines,
			"files_with_matched": summary.FilesWithMatched,
			"files_scanned":      summary.FilesScanned,
		}
	}
	result0 := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    builder.String(),
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
	if boolArg(args, "count") {
		raw = string(GrepModeCount)
	}
	if value, ok := args["mode"].(string); ok && strings.TrimSpace(value) != "" {
		raw = value
	}
	mode, err := NormalizeGrepMode(raw)
	if err != nil {
		return "", toolcore.Error(toolcore.ErrInvalidArgument, "use mode=content|files|count", err)
	}
	return mode, nil
}

// grepPageUnit names the pagination unit for the truncation notice.
func grepPageUnit(mode GrepMode) string {
	if mode == GrepModeContent {
		return "matches"
	}
	return "files"
}

func grepRootLabels(paths toolcore.PathResolver, roots []string) []string {
	labels := make([]string, 0, len(roots))
	for _, root := range roots {
		labels = append(labels, paths.Relative(root))
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
