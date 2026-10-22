package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

const remoteLsDefaultLimit = 200

// remoteLsTool lists one directory inside the workspace jail.
type remoteLsTool struct {
	*toolkit.BaseTool
	remoteToolBase
}

func newRemoteLsTool(cfg *Config, workspace *Workspace) *remoteLsTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to list, relative to the remote workspace. Default: workspace root.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum entries to return. Default 200.",
			},
		},
		"additionalProperties": false,
	}
	tool := &remoteLsTool{remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolLs,
		tool.describe("List a remote directory (one level) with entry type and size."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteLsTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["path"].(string)
	target, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	limit := intParam(params, "limit", remoteLsDefaultLimit)
	if limit < 1 {
		limit = remoteLsDefaultLimit
	}

	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return remoteErrorResult(newToolError(CodeNotFound, "verify the workspace path", "directory not found: %s", t.cleanDisplayPath(target))), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check directory permissions", "cannot list %s: %v", t.cleanDisplayPath(target), readErr)), nil
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	var builder strings.Builder
	display := t.cleanDisplayPath(target)
	if display == "" {
		display = "."
	}
	fmt.Fprintf(&builder, "=== %s ===\n", display)
	dirs, files := 0, 0
	printed := 0
	for _, entry := range entries {
		if printed >= limit {
			break
		}
		info, infoErr := entry.Info()
		name := entry.Name()
		if entry.IsDir() {
			dirs++
			fmt.Fprintf(&builder, "d  %s/\n", name)
		} else {
			files++
			size := int64(-1)
			if infoErr == nil {
				size = info.Size()
			}
			switch {
			case size < 0:
				fmt.Fprintf(&builder, "f  %s\n", name)
			default:
				fmt.Fprintf(&builder, "f  %-48s %10d\n", name, size)
			}
		}
		printed++
	}
	if len(entries) == 0 {
		builder.WriteString("(empty directory)\n")
	}
	fmt.Fprintf(&builder, "\n[entries=%d dirs=%d files=%d]\n", len(entries), dirs, files)
	if len(entries) > printed {
		fmt.Fprintf(&builder, "[list truncated: showing %d of %d; raise limit or list a subdirectory]\n", printed, len(entries))
	}

	metadata := map[string]interface{}{
		"path":         display,
		"entries":      len(entries),
		"dirs":         dirs,
		"files":        files,
		"returned":     printed,
		"is_truncated": len(entries) > printed,
		"remote":       true,
		"workspace":    t.workspace.Root(),
	}
	body, metadata := t.shapeModelOutput(ToolLs, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}

const remoteGlobDefaultLimit = 500

// remoteGlobTool expands a glob pattern relative to the workspace.
type remoteGlobTool struct {
	*toolkit.BaseTool
	remoteToolBase
}

func newRemoteGlobTool(cfg *Config, workspace *Workspace) *remoteGlobTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern, e.g. **/*.go. Relative to the remote workspace.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Base directory for the pattern. Default: workspace root.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum matches to return. Default 500.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
	tool := &remoteGlobTool{remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolGlob,
		tool.describe("Find remote files by glob pattern; respects the workspace boundary."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteGlobTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	pattern, _ := params["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass pattern", "pattern is required")), nil
	}
	rawPath, _ := params["path"].(string)
	base, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	limit := intParam(params, "limit", remoteGlobDefaultLimit)
	if limit < 1 {
		limit = remoteGlobDefaultLimit
	}

	// filepath.Glob does not support **; walk and match the user pattern with
	// the simple-segment semantics the model expects for * and ?.
	matcher, matchErr := newGlobMatcher(pattern)
	if matchErr != nil {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check the pattern syntax", "invalid pattern %q: %v", pattern, matchErr)), nil
	}

	var matches []string
	walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, never abort the whole search
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipRemoteDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		if matcher.match(filepath.ToSlash(rel)) {
			matches = append(matches, path)
			if len(matches) >= limit+1 {
				return errStopWalk
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != errStopWalk {
		if ctx != nil && ctx.Err() != nil {
			return remoteErrorResult(contextErr(ctx)), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "narrow the base path", "walk %s: %v", t.cleanDisplayPath(base), walkErr)), nil
	}

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	sort.Strings(matches)

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== glob %q in %s ===\n", pattern, t.cleanDisplayPath(base))
	for _, match := range matches {
		builder.WriteString(t.cleanDisplayPath(match))
		builder.WriteByte('\n')
	}
	if len(matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern]\n", limit)
	}

	metadata := map[string]interface{}{
		"pattern":      pattern,
		"path":         t.cleanDisplayPath(base),
		"matches":      len(matches),
		"is_truncated": truncated,
		"remote":       true,
		"workspace":    t.workspace.Root(),
	}
	body, metadata := t.shapeModelOutput(ToolGlob, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}

// errStopWalk is an internal sentinel used to stop WalkDir early.
var errStopWalk = fmt.Errorf("stop walk")

// remoteGrepTool searches file contents inside the workspace.
type remoteGrepTool struct {
	*toolkit.BaseTool
	remoteToolBase
}

const (
	remoteGrepDefaultLimit = 200
	remoteGrepMaxLineBytes = 2048
)

func newRemoteGrepTool(cfg *Config, workspace *Workspace) *remoteGrepTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regular expression to search for (ripgrep syntax).",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search. Default: workspace root.",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Optional file filter, e.g. *.go.",
			},
			"case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "Case-insensitive match.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum matches to return. Default 200.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
	tool := &remoteGrepTool{remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolGrep,
		tool.describe("Search remote file contents with a regular expression; results are file:line:text."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteGrepTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	pattern, _ := params["pattern"].(string)
	if strings.TrimSpace(pattern) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass pattern", "pattern is required")), nil
	}
	caseInsensitive, _ := params["case_insensitive"].(bool)
	compiled, compileErr := compileRemoteRegexp(pattern, caseInsensitive)
	if compileErr != nil {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check the regular expression syntax", "invalid pattern %q: %v", pattern, compileErr)), nil
	}
	rawPath, _ := params["path"].(string)
	target, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	fileGlob, _ := params["glob"].(string)
	var globMatcher *globMatcher
	if strings.TrimSpace(fileGlob) != "" {
		globMatcher, err = newGlobMatcher(fileGlob)
		if err != nil {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "check the glob syntax", "invalid glob %q: %v", fileGlob, err)), nil
		}
	}
	limit := intParam(params, "limit", remoteGrepDefaultLimit)
	if limit < 1 {
		limit = remoteGrepDefaultLimit
	}

	type match struct {
		file string
		line int
		text string
	}
	var matches []match
	scannedFiles := 0

	visit := func(path string) error {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > int64(t.maxViewBytes()) {
			return nil
		}
		scannedFiles++
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		reader := newLineReader(file)
		for {
			lineNo, text, ok := reader.next()
			if !ok {
				return nil
			}
			if len(text) > remoteGrepMaxLineBytes {
				text = text[:remoteGrepMaxLineBytes] + "…"
			}
			if !utf8ValidString(text) {
				// Binary content: skip the rest of this file.
				return nil
			}
			if compiled.MatchString(text) {
				matches = append(matches, match{file: path, line: lineNo, text: text})
				if len(matches) > limit {
					return errStopWalk
				}
			}
		}
	}

	singleFile := false
	if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() {
		singleFile = true
		if globMatcher == nil || globMatcher.match(filepath.Base(target)) {
			if visitErr := visit(target); visitErr != nil && visitErr != errStopWalk {
				return remoteErrorResult(contextErr(ctx)), nil
			}
		}
	} else {
		walkErr := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipRemoteDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if globMatcher != nil && !globMatcher.match(entry.Name()) {
				return nil
			}
			return visit(path)
		})
		if walkErr != nil && walkErr != errStopWalk && !(ctx != nil && ctx.Err() != nil) {
			return remoteErrorResult(newToolError(CodeInvalidArgument, "narrow the search path", "walk %s: %v", t.cleanDisplayPath(target), walkErr)), nil
		}
	}

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== grep %q ===\n", pattern)
	for _, item := range matches {
		fmt.Fprintf(&builder, "%s:%d:%s\n", t.cleanDisplayPath(item.file), item.line, item.text)
	}
	if len(matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern or glob]\n", limit)
	}

	metadata := map[string]interface{}{
		"pattern":       pattern,
		"path":          t.cleanDisplayPath(target),
		"matches":       len(matches),
		"files_scanned": scannedFiles,
		"single_file":   singleFile,
		"is_truncated":  truncated,
		"remote":        true,
		"workspace":     t.workspace.Root(),
	}
	body, metadata := t.shapeModelOutput(ToolGrep, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}

// shouldSkipRemoteDir skips VCS and dependency trees that would dominate a walk.
func shouldSkipRemoteDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "__pycache__", ".idea", ".gradle":
		return true
	default:
		return false
	}
}
