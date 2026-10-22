package lscore

import (
	"context"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// NewHandler builds the surface-independent ls Handler. The caller supplies
// the Definition (schema, description, aliases) and injects a Runtime per call;
// this keeps one listing implementation while local/remote surfaces keep their
// own schemas and output policy.
func NewHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "ls"
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
	rawPath, _ := call.Args["path"].(string)
	abs, err := paths.Resolve(rawPath)
	if err != nil {
		return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
	}
	if permissions := call.Runtime.Permissions(); permissions != nil {
		if err := permissions.CheckPath(toolcore.OpRead, abs); err != nil {
			return toolcore.Error(toolcore.ErrPathDenied, "use a path inside the workspace", err), nil
		}
	}
	limit := intArg(call.Args, "limit", DefaultLimit)
	listing, readErr := Read(abs, limit)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the workspace path", readErr), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check directory permissions", readErr), nil
	}

	display := paths.Relative(abs)
	result := &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    Render(display, listing),
		Metadata:   Metadata(display, listing),
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
