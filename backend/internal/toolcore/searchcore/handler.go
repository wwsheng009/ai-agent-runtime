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
	target, err := paths.Resolve(stringArg(call.Args, "path"))
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, target); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	options := GrepOptions{
		Patterns:        patterns,
		Path:            target,
		Glob:            stringArg(call.Args, "glob"),
		CaseInsensitive: boolArg(call.Args, "case_insensitive"),
		Literal:         boolArg(call.Args, "literal", "fixed_strings"),
		IgnoreCase:      boolArg(call.Args, "ignore_case"),
		CaseSensitive:   boolArg(call.Args, "case_sensitive"),
		SmartCase:       boolArg(call.Args, "smart_case"),
		Word:            boolArg(call.Args, "word", "word_regexp"),
		LineRegexp:      boolArg(call.Args, "line_regexp"),
		InvertMatch:     boolArg(call.Args, "invert_match"),
		Limit:           intArg(call.Args, "limit", DefaultGrepLimit),
		MaxFileBytes:    captureLimit(call),
		Offset:          intArg(call.Args, "offset", 0),
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
			fmt.Errorf("grep %q in %s: %v", strings.Join(patterns, " | "), paths.Relative(target), grepErr)), nil
	}

	patternLabel := strings.Join(patterns, " | ")
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== grep %q ===\n", patternLabel)
	for _, item := range result.Matches {
		fmt.Fprintf(&builder, "%s:%d:%s\n", paths.Relative(item.File), item.Line, item.Text)
	}
	if len(result.Matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if result.Truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern or glob]\n", options.Limit)
	}
	metadata := map[string]any{
		"pattern":       patternLabel,
		"patterns":      patterns,
		"path":          paths.Relative(target),
		"matches":       len(result.Matches),
		"files_scanned": result.FilesScanned,
		"single_file":   result.SingleFile,
		"is_truncated":  result.Truncated,
		"invert_match":  options.InvertMatch,
		"offset":        options.Offset,
		"next_offset":   options.Offset + len(result.Matches),
		"has_more":      result.Truncated,
		"engine":        engine,
		"hidden_files":  true,
		"ignore_files":  false,
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

// boolArg reports whether any of the listed spellings is set to true.
func boolArg(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := args[key].(bool); ok && value {
			return true
		}
	}
	return false
}
