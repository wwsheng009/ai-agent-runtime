package viewcore

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/pathguard"
)

const (
	// batchDefaultLimit matches the builtin view batch default: batch calls are
	// for scanning many files, not for pulling whole files.
	batchDefaultLimit = 200
	// compactHeadLines matches the builtin view compact head window.
	compactHeadLines = 10
	// DefaultByteBudget is the window one read may spend when the surface does
	// not set one: the tool's own model-visible budget. A zero Options.ByteBudget
	// must not silently degrade into "one line per window".
	DefaultByteBudget = toolcoreoutput.ViewOutputBudgetBytes
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

	// The name guard runs before resolution on purpose: a surface whose
	// resolver refuses device paths itself would otherwise answer with its own
	// refusal shape, and one path would be refused two different ways depending
	// on where it ran.
	if len(requests) == 1 {
		if reason := pathguard.NameReason(requests[0].FilePath); reason != "" {
			return shapeResult(call, h.def.Name, RefusalResult(requests[0].FilePath, reason)), nil
		}
	}

	if len(requests) == 1 && !hasFilesArg(call.Args) {
		result := h.readTarget(call, paths, requests[0], DefaultLimit)
		return shapeResult(call, h.def.Name, result), nil
	}

	result := h.readBatch(call, paths, requests, compact)
	return shapeResult(call, h.def.Name, result), nil
}

func viewRequests(args map[string]any) ([]Request, bool) {
	compact := boolArg(args, "compact", false)
	requests := make([]Request, 0, 1)
	if rawPath, _ := args["file_path"].(string); strings.TrimSpace(rawPath) != "" {
		requests = append(requests, Request{
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
		requests = append(requests, Request{
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
func (h *handler) readTarget(call *toolcore.Call, paths toolcore.PathResolver, req Request, defaultLimit int) *toolcore.Result {
	abs, err := paths.Resolve(req.FilePath)
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err)
	}
	// Device / FIFO / reserved names are refused before any I/O: opening a FIFO
	// blocks until a writer appears and /dev/zero streams meaningless bytes. The
	// check runs ahead of the permission hook so the refusal keeps the
	// view-specific shape (path_refused / refusal_reason) on every surface.
	if reason := pathguard.NameReason(abs); reason != "" {
		return RefusalResult(abs, reason)
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
	// A stat result is safe; the refusal here is what keeps a character device
	// or named pipe from ever reaching open.
	if reason := pathguard.ModeReason(info.Mode()); reason != "" {
		return RefusalResult(abs, reason)
	}
	if info.IsDir() {
		// Pointing view at a directory lists it instead of failing: the caller
		// wanted to know what is there, and the metadata says so explicitly.
		listing, listErr := DirectoryListing(abs, req.FilePath)
		if listErr != nil {
			return toolcore.Error(toolcore.ErrInvalidArgument, "use ls for directories", listErr)
		}
		return listing
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	offset, limit := Normalize(req.Offset, limit)
	window := Request{FilePath: req.FilePath, Offset: offset, Limit: limit}
	content, stats, readErr := ReadText(abs, window, Options{ByteBudget: byteBudget(call, h.def)})
	if readErr != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the file and narrow the window", readErr)
	}
	// A window that is mostly NUL bytes is a binary file: describe it (MIME,
	// size, recovery route) instead of handing the model unreadable content.
	if IsBinaryContent(content) {
		return BinaryNote(abs, req.FilePath, info.Size())
	}
	return BuildResult(abs, info.Size(), window, content, stats)
}

// readBatch reads every request and renders one section per file. A failed
// file becomes an error section plus metadata instead of failing the batch.
func (h *handler) readBatch(call *toolcore.Call, paths toolcore.PathResolver, requests []Request, compact bool) *toolcore.Result {
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
		body := section.Content
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
				body += compactSummary(section.Metadata)
			}
		} else {
			failures++
			meta["error"] = section.ErrorCode
			message := strings.TrimSpace(section.Content)
			if message == "" {
				message = section.ErrorCode
			}
			body = fmt.Sprintf("[error: %s]", message)
		}
		// One section per file, headed by the path the caller asked for: the
		// shape every view surface has always produced.
		if index > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "===== %s =====\n%s", req.FilePath, body)
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

// byteBudget resolves the byte budget one window may spend. Every surface reads
// the same budget: the tool's own declared window, which is what the result is
// allowed to deliver. A surface may raise it through CaptureLimit when its echo
// layer folds later than the tool's own stop condition.
func byteBudget(call *toolcore.Call, def toolcore.Definition) int {
	if call != nil && call.Runtime != nil {
		if provider, ok := call.Runtime.(toolcore.CaptureBudgetProvider); ok {
			if limit := provider.CaptureLimit("view"); limit > 0 {
				return limit
			}
		}
	}
	if def.DefaultOutputBudget > 0 {
		return def.DefaultOutputBudget
	}
	return DefaultByteBudget
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
