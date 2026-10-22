package viewcore

import (
	"context"
	"fmt"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
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
	rawPath, _ := call.Args["file_path"].(string)
	if rawPath == "" {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass file_path", nil), nil
	}
	abs, err := paths.Resolve(rawPath)
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, abs); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the path with ls or glob", statErr), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check permissions on the target file", statErr), nil
	}
	if info.IsDir() {
		return toolcore.Error(toolcore.ErrInvalidArgument, "use ls for directories", fmt.Errorf("%s is a directory", paths.Relative(abs))), nil
	}
	if !info.Mode().IsRegular() {
		return toolcore.Error(toolcore.ErrInvalidArgument, "use a regular file",
			fmt.Errorf("%s is not a regular file", paths.Relative(abs))), nil
	}

	offset := intArg(call.Args, "offset", 0)
	limit := intArg(call.Args, "limit", DefaultLimit)
	offset, limit = Normalize(offset, limit)
	window, readErr := Read(abs, offset, limit, maxBytes(call))
	if readErr != nil {
		return toolcore.Error(toolcore.ErrInvalidArgument, "check the file and narrow the window", readErr), nil
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
	if shaper := call.Runtime.Output(); shaper != nil {
		shaped, shapeErr := shaper.Shape(h.def.Name, result)
		if shapeErr != nil {
			return toolcore.Error(toolcore.ErrInternal, "retry the call", shapeErr), nil
		}
		if shaped != nil {
			result = shaped
		}
	}
	return result, nil
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
