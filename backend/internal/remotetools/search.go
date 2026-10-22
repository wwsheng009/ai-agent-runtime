package remotetools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

const remoteLsDefaultLimit = lscore.DefaultLimit

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
	listing, readErr := lscore.Read(target, limit)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return remoteErrorResult(newToolError(CodeNotFound, "verify the workspace path", "directory not found: %s", t.cleanDisplayPath(target))), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check directory permissions", "cannot list %s: %v", t.cleanDisplayPath(target), readErr)), nil
	}

	display := t.cleanDisplayPath(target)
	body := lscore.Render(display, listing)
	metadata := lscore.Metadata(display, listing)
	metadata["remote"] = true
	metadata["workspace"] = t.workspace.Root()
	body, metadata = t.shapeModelOutput(ToolLs, body, metadata)
	return remoteTextResult(body, metadata), nil
}

const remoteGlobDefaultLimit = 500

// remoteGlobTool expands a glob pattern relative to the workspace.
type remoteGlobTool struct {
	*toolkit.BaseTool
	remoteToolBase
	rg ripgrepRunner
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
	tool := &remoteGlobTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		rg:             resolveRipgrepRunner(),
	}
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
	engine := "walker"
	result, rgUsed, globErr := t.rg.glob(ctx, base, pattern, limit)
	if rgUsed && globErr == nil {
		engine = "ripgrep"
	} else {
		if ctx != nil && ctx.Err() != nil {
			return remoteErrorResult(contextErr(ctx)), nil
		}
		result, globErr = searchcore.Glob(ctx, base, pattern, limit)
	}
	if globErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return remoteErrorResult(contextErr(ctx)), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check the pattern syntax and narrow the base path", "glob %q in %s: %v", pattern, t.cleanDisplayPath(base), globErr)), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== glob %q in %s ===\n", pattern, t.cleanDisplayPath(base))
	for _, match := range result.Matches {
		builder.WriteString(t.cleanDisplayPath(match))
		builder.WriteByte('\n')
	}
	if len(result.Matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if result.Truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern]\n", limit)
	}

	metadata := map[string]interface{}{
		"pattern":      pattern,
		"path":         t.cleanDisplayPath(base),
		"matches":      len(result.Matches),
		"is_truncated": result.Truncated,
		"engine":       engine,
		"hidden_files": true,
		"ignore_files": false,
		"remote":       true,
		"workspace":    t.workspace.Root(),
	}
	body, metadata := t.shapeModelOutput(ToolGlob, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}

// remoteGrepTool searches file contents inside the workspace.
type remoteGrepTool struct {
	*toolkit.BaseTool
	remoteToolBase
	rg ripgrepRunner
}

const remoteGrepDefaultLimit = searchcore.DefaultGrepLimit

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
	tool := &remoteGrepTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		rg:             resolveRipgrepRunner(),
	}
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
	rawPath, _ := params["path"].(string)
	target, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	fileGlob, _ := params["glob"].(string)
	limit := intParam(params, "limit", remoteGrepDefaultLimit)
	options := searchcore.GrepOptions{
		Pattern:         pattern,
		Path:            target,
		Glob:            fileGlob,
		CaseInsensitive: caseInsensitive,
		Limit:           limit,
		MaxFileBytes:    t.maxViewBytes(),
	}
	engine := "walker"
	result, rgUsed, grepErr := t.rg.grep(ctx, options)
	if rgUsed && grepErr == nil {
		engine = "ripgrep"
	} else {
		if ctx != nil && ctx.Err() != nil {
			return remoteErrorResult(contextErr(ctx)), nil
		}
		result, grepErr = searchcore.Grep(ctx, options)
	}
	if grepErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return remoteErrorResult(contextErr(ctx)), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check the regex/glob syntax and narrow the path", "grep %q in %s: %v", pattern, t.cleanDisplayPath(target), grepErr)), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== grep %q ===\n", pattern)
	for _, item := range result.Matches {
		fmt.Fprintf(&builder, "%s:%d:%s\n", t.cleanDisplayPath(item.File), item.Line, item.Text)
	}
	if len(result.Matches) == 0 {
		builder.WriteString("(no matches)\n")
	}
	if result.Truncated {
		fmt.Fprintf(&builder, "\n[truncated at %d matches; narrow the pattern or glob]\n", limit)
	}

	metadata := map[string]interface{}{
		"pattern":       pattern,
		"path":          t.cleanDisplayPath(target),
		"matches":       len(result.Matches),
		"files_scanned": result.FilesScanned,
		"single_file":   result.SingleFile,
		"is_truncated":  result.Truncated,
		"engine":        engine,
		"hidden_files":  true,
		"ignore_files":  false,
		"remote":        true,
		"workspace":     t.workspace.Root(),
	}
	body, metadata := t.shapeModelOutput(ToolGrep, builder.String(), metadata)
	return remoteTextResult(body, metadata), nil
}
