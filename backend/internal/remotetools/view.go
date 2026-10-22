package remotetools

import (
	"context"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// View defaults mirror the local view tool so a remote read and a local read of
// the same file produce the same window and continuation hints.
// remoteViewTool reads one file window inside the workspace jail.
//
// It intentionally does not reuse toolkit/tools.ViewTool: that tool carries
// local-only concerns (symbol index, image attachments, read dedup ledger, LSP
// hooks) that would either be wrong on a remote host or add a large dependency
// tree to the small binary. The wire contract - line numbers, is_truncated,
// suggested_next_offset - is kept identical.
type remoteViewTool struct {
	*toolkit.BaseTool
	remoteToolBase
}

func newRemoteViewTool(cfg *Config, workspace *Workspace) *remoteViewTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "File to read, relative to the remote workspace (or an absolute path inside it).",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "1-based line number to start from. Default 1.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"maximum":     viewcore.MaxLimit,
				"description": "Maximum lines to return. Default 400, capped at 2000.",
			},
		},
		"required":             []string{"file_path"},
		"additionalProperties": false,
	}
	tool := &remoteViewTool{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
	}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolView,
		tool.describe("Read a window of a remote text file with stable line numbers and truncation metadata."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteViewTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPath, _ := params["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass file_path", "file_path is required")), nil
	}
	target, err := t.resolve(ctx, rawPath)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if err := ensureNotDevice(target); err != nil {
		return remoteErrorResult(err), nil
	}

	info, statErr := os.Stat(target)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return remoteErrorResult(newToolError(CodeNotFound, "verify the path with ls or glob", "file not found: %s", t.cleanDisplayPath(target))), nil
		}
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check permissions on the target file", "cannot stat %s: %v", t.cleanDisplayPath(target), statErr)), nil
	}
	if info.IsDir() {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "use ls for directories", "%s is a directory", t.cleanDisplayPath(target))), nil
	}

	offset := intParam(params, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := intParam(params, "limit", viewcore.DefaultLimit)
	offset, limit = viewcore.Normalize(offset, limit)
	window, readErr := viewcore.Read(target, offset, limit, t.maxViewBytes())
	if readErr != nil {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "check the file and narrow the window", "read %s: %v", t.cleanDisplayPath(target), readErr)), nil
	}

	display := t.cleanDisplayPath(target)
	body := viewcore.Render(display, window)
	metadata := viewcore.Metadata(display, window)
	metadata["remote"] = true
	metadata["workspace"] = t.workspace.Root()
	if !viewcore.ValidUTF8(body) {
		metadata["binary"] = true
	}
	body, metadata = t.shapeModelOutput(ToolView, body, metadata)
	return remoteTextResult(body, metadata), nil
}

// intParam reads an integer argument that may arrive as int/float64/json.Number.
func intParam(params map[string]interface{}, key string, fallback int) int {
	raw, ok := params[key]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
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
