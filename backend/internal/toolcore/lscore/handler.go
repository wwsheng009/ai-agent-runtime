package lscore

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Parameters is the schema both surfaces publish. There is one ls tool, so the
// local tree listing (depth) and the remote one-level listing are the same
// capability: depth defaults to 1, which is exactly the old remote behavior.
func Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "目录路径，默认为当前目录。多个独立目录可在同一 assistant 响应中并行发起 ls 调用。",
			},
			"depth": map[string]interface{}{
				"type":        "integer",
				"description": "递归深度（默认为 1，只显示当前目录）。若目录结构很大，建议保持较小深度并分次查看不同目录。",
				"minimum":     1,
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "最多返回的条目数（默认 1000；单层列表默认 200）。",
			},
		},
		"required": []string{},
	}
}

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
	display := paths.Relative(abs)
	// The existence check goes through the I/O seam: which machine this describes
	// is the Runtime's decision, not this core's.
	access := localio.For(call.Runtime)
	info, statErr := access.Stat(ctx, abs)
	if statErr != nil {
		if toolcore.IsNotExist(statErr) {
			hint := ""
			if hinter, ok := call.Runtime.(toolcore.PathHinter); ok {
				hint = hinter.PathHint(rawPath)
			}
			message := fmt.Errorf("路径不存在: %s", display)
			if hint != "" {
				message = fmt.Errorf("路径不存在: %s\n%s", display, hint)
			}
			return toolcore.Error(toolcore.ErrNotFound, "verify the path with glob or grep", message), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check directory permissions",
			fmt.Errorf("无法访问路径 %s: %w", display, statErr)), nil
	}
	// A file path is a valid ls argument: it reports that one file's size and
	// mode instead of failing or listing its parent. The local tool has always
	// behaved this way, and `ls <file>` is how a caller confirms a path exists.
	if !info.IsDir() {
		metadata := map[string]interface{}{
			"path":           display,
			"isDir":          false,
			"size":           info.Size(),
			"mode":           info.Mode().String(),
			"returned_count": 1,
			"result_count":   1,
		}
		return h.shape(call, &toolcore.Result{
			Success:    true,
			OutputKind: toolcore.KindText,
			Content:    fmt.Sprintf("%s (%d bytes, %s)", display, info.Size(), info.Mode().String()),
			Metadata:   metadata,
		}), nil
	}

	depth := intArg(call.Args, "depth", 1)
	if depth < 1 {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass depth >= 1, or omit it for one level",
			fmt.Errorf("depth 参数必须大于 0")), nil
	}
	if depth > MaxDepth {
		depth = MaxDepth
	}
	// One level keeps the historical one-directory page size; any deeper walk
	// uses the tree default, because the same number would otherwise describe
	// wildly different amounts of work.
	defaultLimit := DefaultTreeLimit
	if depth == 1 {
		defaultLimit = DefaultLimit
	}
	limit := intArg(call.Args, "limit", defaultLimit)
	if limit < 1 {
		return toolcore.Error(toolcore.ErrInvalidArgument, "pass limit >= 1, or omit it for the default",
			fmt.Errorf("limit 参数必须大于 0")), nil
	}

	listing, readErr := ReadTree(abs, depth, limit)
	if readErr != nil {
		if toolcore.IsNotExist(readErr) {
			return toolcore.Error(toolcore.ErrNotFound, "verify the workspace path", readErr), nil
		}
		return toolcore.Error(toolcore.ErrInvalidArgument, "check directory permissions", readErr), nil
	}
	content, rendered, byteBudgetHit := RenderTree(display, listing, OutputBudgetBytes)
	truncated := listing.Truncated || byteBudgetHit
	metadata := map[string]interface{}{
		"path":           display,
		"depth":          depth,
		"file_count":     listing.Files,
		"dir_count":      listing.Dirs,
		"total":          listing.Total,
		"returned_count": rendered,
		"result_count":   rendered,
		"truncated":      truncated,
		"limit_hit":      listing.LimitHit,
		// Compatibility with the one-level listing shape.
		"entries":      listing.Total,
		"dirs":         listing.Dirs,
		"files":        listing.Files,
		"returned":     rendered,
		"is_truncated": truncated,
	}
	if truncated {
		metadata[toolresult.MetadataNextActionKey] = "目录条目超过 ls 预算已截断；请缩小 depth、分目录查看，或用 glob/grep 精确定位。不要原样重试同一路径。"
	}
	if listing.Total == 0 {
		toolresult.MarkEmptySuccess(metadata)
	}
	return h.shape(call, &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    content,
		Metadata:   metadata,
	}), nil
}

func (h *handler) shape(call *toolcore.Call, result *toolcore.Result) *toolcore.Result {
	if call == nil || result == nil || !result.Success {
		return result
	}
	if shaper := call.Runtime.Output(); shaper != nil {
		shaped, shapeErr := shaper.Shape(h.def.Name, result)
		if shapeErr != nil {
			return toolcore.Error(toolcore.ErrInternal, "retry the call", shapeErr)
		}
		if shaped != nil {
			return shaped
		}
	}
	return result
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
