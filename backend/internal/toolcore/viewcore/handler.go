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
		result, delivered := h.readTarget(call, paths, requests[0], DefaultLimit)
		// A single read is delivered as soon as it is produced: there is no
		// aggregate cap that could drop it.
		delivered.Commit()
		return shapeResult(call, h.def.Name, result), nil
	}

	result := ReadBatch(requests, compact, func(req Request) (*toolcore.Result, *DeliveredRead) {
		return h.readTarget(call, paths, req, BatchDefaultLimit)
	})
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
//
// The second return value is the host bookkeeping this read owes (a read ledger
// entry, a delivered-window cache entry); it is nil when the surface has no such
// state. Callers commit it once the result is really delivered.
func (h *handler) readTarget(call *toolcore.Call, paths toolcore.PathResolver, req Request, defaultLimit int) (*toolcore.Result, *DeliveredRead) {
	abs, err := paths.Resolve(req.FilePath)
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	// Device / FIFO / reserved names are refused before any I/O: opening a FIFO
	// blocks until a writer appears and /dev/zero streams meaningless bytes. The
	// check runs ahead of the permission hook so the refusal keeps the
	// view-specific shape (path_refused / refusal_reason) on every surface.
	if reason := pathguard.NameReason(abs); reason != "" {
		return RefusalResult(abs, reason), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, abs); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	info, statErr := os.Stat(abs)
	healed := false
	if statErr != nil && os.IsNotExist(statErr) {
		// An invisible-spelling mismatch (case or Unicode variants) makes the
		// existence check fail forever while the file is right there, so a unique
		// candidate is adopted and the read proceeds.
		if candidate, candidates := HealSpelling(permissionChecker(call), abs); candidate != "" {
			abs = candidate
			healed = true
			info, statErr = os.Stat(abs)
		} else if len(candidates) > 1 {
			return AmbiguousSpellingResult(req.FilePath, abs, candidates), nil
		}
	}
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the path with ls or glob", pathNotFoundError(call, "路径不存在", req.FilePath)), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check permissions on the target file", statErr), nil
	}
	// A stat result is safe; the refusal here is what keeps a character device
	// or named pipe from ever reaching open.
	if reason := pathguard.ModeReason(info.Mode()); reason != "" {
		return RefusalResult(abs, reason), nil
	}
	if info.IsDir() {
		// Pointing view at a directory lists it instead of failing: the caller
		// wanted to know what is there, and the metadata says so explicitly.
		listing, listErr := DirectoryListing(abs, req.FilePath)
		if listErr != nil {
			return toolcore.Error(toolcore.ErrInvalidArgument, "use ls for directories", listErr), nil
		}
		return listing, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	offset, limit := Normalize(req.Offset, limit)
	window := Request{FilePath: req.FilePath, Offset: offset, Limit: limit}
	observer := readObserver(call)
	// A complete window the model already saw can answer for itself. The peek
	// runs before the read so a repeat costs nothing; the entry is consumed only
	// once the stub is really delivered.
	if observer != nil {
		if stub, consume, hit := observer.RememberedStub(h.def.Name, abs, offset, limit); hit {
			delivered := &DeliveredRead{}
			delivered.Defer(consume)
			return stub, delivered
		}
	}

	content, stats, readErr := ReadText(abs, window, Options{ByteBudget: byteBudget(call, h.def)})
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the path with ls or glob", pathNotFoundError(call, "读取文件失败", req.FilePath)), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the file and narrow the window", readErr), nil
	}
	// A window that is mostly NUL bytes is a binary file: describe it (MIME,
	// size, recovery route) instead of handing the model unreadable content.
	if IsBinaryContent(content) {
		return BinaryNote(abs, req.FilePath, info.Size()), nil
	}
	result := BuildResult(abs, info.Size(), window, content, stats)
	if healed {
		// The caller asked for a spelling that did not exist; say which path was
		// really read so the next call uses it instead of repeating the mismatch.
		result.Metadata["path_auto_healed"] = true
		result.Metadata["original_path"] = req.FilePath
		result.Metadata["resolved_path"] = abs
		appendNote(&result.Content, fmt.Sprintf(
			"[note] 路径 %q 按磁盘实际拼写解析为 %q（大小写/Unicode 差异）。后续调用请使用解析后的路径。",
			req.FilePath, abs,
		))
	}
	if observer == nil {
		return result, nil
	}

	deliveredWindow := DeliveredWindow(window, stats, info.Size())
	delivered := &DeliveredRead{}
	delivered.Defer(func() {
		observer.ReadDelivered(h.def.Name, abs, deliveredWindow)
	})
	// Only a complete window is worth remembering: a byte-budget stop, a line
	// clamp or a tail window means a later stub would hide content the model
	// never saw.
	if !stats.Tail && stats.LinesRead > 0 && !stats.ByteBudgetApplied &&
		stats.LongLinesTruncated == 0 && !stats.EmptyFile {
		delivered.Defer(func() {
			observer.ReadRemembered(h.def.Name, abs, deliveredWindow)
		})
	}
	return result, delivered
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

// permissionChecker returns the surface's authorization hook, if it has one.
// A runtime without one admits every already-resolved path.
func permissionChecker(call *toolcore.Call) toolcore.PermissionChecker {
	if call == nil || call.Runtime == nil {
		return nil
	}
	return call.Runtime.Permissions()
}

// pathNotFoundError renders the "path not found" sentence every file tool
// publishes: a prefix, the spelling the caller used, and the candidate hint the
// surface can compute. Candidates depend on each surface's root, so the runtime
// supplies them through PathHinter and the shared handler only formats the text.
func pathNotFoundError(call *toolcore.Call, prefix, raw string) error {
	if call != nil && call.Runtime != nil {
		if hinter, ok := call.Runtime.(toolcore.PathHinter); ok {
			if hint := hinter.PathHint(raw); hint != "" {
				return fmt.Errorf("%s: %s\n%s", prefix, raw, hint)
			}
		}
	}
	return fmt.Errorf("%s: %s", prefix, raw)
}

// readObserver returns the surface's read-state extension, if it has one. A
// runtime without a session ledger or a delivered-window cache simply has no
// such state to maintain and reads stay stateless.
func readObserver(call *toolcore.Call) toolcore.ReadObserver {
	if call == nil || call.Runtime == nil {
		return nil
	}
	observer, _ := call.Runtime.(toolcore.ReadObserver)
	return observer
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
