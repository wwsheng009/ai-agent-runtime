package viewcore

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

const (
	// batchDefaultLimit matches the builtin view batch default: batch calls are
	// for scanning many files, not for pulling whole files.
	batchDefaultLimit = 200
	// compactHeadLines matches the builtin view compact head window.
	compactHeadLines = 10
)

// NewHandler builds the shared view Handler. The Definition carries the
// surface schema/description; Runtime injects path resolution, permission,
// output shaping and request-scoped workspace state.
func NewHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "view"
	}
	if def.Version == "" {
		def.Version = "1.0.0"
	}
	return &handler{def: def}
}

type handler struct {
	def toolcore.Definition
}

func (h *handler) Definition() toolcore.Definition { return h.def }

func (h *handler) Execute(ctx context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	if call == nil || call.Runtime == nil {
		return toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil), nil
	}
	paths := call.Runtime.Paths()
	if paths == nil {
		return toolcore.Error(toolcore.ErrInternal, "fix the runtime binding, then retry", nil), nil
	}

	requests, compact := viewRequests(call.Args)
	if len(requests) == 0 {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass file_path or files", nil), nil
	}

	if len(requests) == 1 && !hasFilesArg(call.Args) {
		result := h.readTarget(call, paths, requests[0], DefaultLimit)
		return shapeResult(call, h.def.Name, result), nil
	}

	result := h.readBatch(call, paths, requests, compact)
	return shapeResult(call, h.def.Name, result), nil
}

// viewRequest is one file window. The same shape backs file_path and every
// files[] entry, so single and batch reads share one code path.
type viewRequest struct {
	FilePath string
	Offset   int
	Limit    int
}

func viewRequests(args map[string]any) ([]viewRequest, bool) {
	compact := boolArg(args, "compact", false)
	requests := make([]viewRequest, 0, 1)
	if rawPath, _ := args["file_path"].(string); strings.TrimSpace(rawPath) != "" {
		requests = append(requests, viewRequest{
			FilePath: rawPath,
			Offset:   intArg(args, "offset", 0),
			Limit:    intArg(args, "limit", 0),
		})
	}
	for _, item := range argSlice(args["files"]) {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rawPath, _ := entry["file_path"].(string)
		if strings.TrimSpace(rawPath) == "" {
			continue
		}
		requests = append(requests, viewRequest{
			FilePath: rawPath,
			Offset:   intArg(entry, "offset", 0),
			Limit:    intArg(entry, "limit", 0),
		})
	}
	return requests, compact
}

func hasFilesArg(args map[string]any) bool { return len(argSlice(args["files"])) > 0 }

func argSlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		items := make([]any, len(typed))
		for i := range typed {
			items[i] = typed[i]
		}
		return items
	default:
		return nil
	}
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

// readTarget resolves, authorizes, stats and reads one request. Errors are
// returned as toolcore error results so single reads can return them directly
// and batch reads can report one failed file without dropping the batch.
func (h *handler) readTarget(call *toolcore.Call, paths toolcore.PathResolver, req viewRequest, defaultLimit int) *toolcore.Result {
	abs, err := paths.Resolve(req.FilePath)
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, abs); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
		}
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the path with ls or glob", statErr)
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check permissions on the target file", statErr)
	}
	if info.IsDir() {
		return toolcore.Error(toolcore.ErrInvalidArgument, "use ls for directories", fmt.Errorf("%s is a directory", paths.Relative(abs)))
	}
	if !info.Mode().IsRegular() {
		return toolcore.Error(toolcore.ErrInvalidArgument, "use a regular file",
			fmt.Errorf("%s is not a regular file", paths.Relative(abs)))
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	offset, limit := Normalize(req.Offset, limit)
	window, readErr := Read(abs, offset, limit, maxBytes(call))
	if readErr != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the file and narrow the window", readErr)
	}
	display := paths.Relative(abs)
	result := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    Render(display, window),
		Metadata:   Metadata(display, window),
	}
	if !ValidUTF8(result.Content) {
		result.Metadata["binary"] = true
	}
	return result
}

// readBatch reads every request and renders one section per file. A failed
// file becomes an error section plus metadata instead of failing the batch.
func (h *handler) readBatch(call *toolcore.Call, paths toolcore.PathResolver, requests []viewRequest, compact bool) *toolcore.Result {
	perFileLimit := batchDefaultLimit
	if compact {
		perFileLimit = compactHeadLines
	}
	var builder strings.Builder
	files := make([]map[string]any, 0, len(requests))
	failures := 0
	for index, req := range requests {
		section := h.readTarget(call, paths, req, perFileLimit)
		display := req.FilePath
		meta := map[string]any{"index": index, "file_path": display}
		if section.Success {
			if resolved, ok := section.Metadata["file_path"].(string); ok {
				display = resolved
				meta["file_path"] = resolved
			}
			for _, key := range []string{"lines_read", "total_lines", "is_truncated", "suggested_next_offset"} {
				if value, ok := section.Metadata[key]; ok {
					meta[key] = value
				}
			}
			if compact {
				section.Content += compactSummary(section.Metadata)
			}
		} else {
			failures++
			meta["error"] = section.ErrorCode
			message := strings.TrimSpace(section.Content)
			if message == "" {
				message = section.ErrorCode
			}
			section.Content = fmt.Sprintf("=== %s ===\n[error: %s]\n", display, message)
		}
		if index > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(section.Content)
		files = append(files, meta)
	}
	metadata := map[string]any{
		"files": files,
		"count": len(requests),
	}
	if compact {
		metadata["compact"] = true
	}
	if failures > 0 {
		metadata["failed"] = failures
	}
	return &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    builder.String(),
		Metadata:   metadata,
	}
}

func compactSummary(metadata map[string]any) string {
	next := "-"
	if value, ok := metadata["suggested_next_offset"]; ok {
		next = fmt.Sprintf("%v", value)
	}
	return fmt.Sprintf("\n[compact: lines_read=%v total_lines=%v is_truncated=%v suggested_next_offset=%s]\n",
		metadata["lines_read"], metadata["total_lines"], metadata["is_truncated"], next)
}

// shapeResult applies the surface's output shaper to successful reads; error
// results keep the runtime's canonical error envelope untouched.
func shapeResult(call *toolcore.Call, toolName string, result *toolcore.Result) *toolcore.Result {
	if call == nil || result == nil || !result.Success {
		return result
	}
	if shaper := call.Runtime.Output(); shaper != nil {
		shaped, shapeErr := shaper.Shape(toolName, result)
		if shapeErr != nil {
			return toolcore.Error(toolcore.ErrInternal, "retry the call", shapeErr)
		}
		if shaped != nil {
			return shaped
		}
	}
	return result
}

func maxBytes(call *toolcore.Call) int {
	// The capture ceiling is a surface policy, separate from the model-visible
	// output budget. A zero value lets viewcore fall back to its default.
	if call == nil || call.Runtime == nil {
		return 0
	}
	if provider, ok := call.Runtime.(toolcore.CaptureBudgetProvider); ok {
		return provider.CaptureLimit("view")
	}
	return 0
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
