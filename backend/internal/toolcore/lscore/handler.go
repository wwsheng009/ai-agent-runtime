package lscore

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/localio"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Parameters is the schema both surfaces publish. There is one list_dir tool, so the
// local tree listing (depth) and the remote one-level listing are the same
// capability: depth defaults to 1, which is exactly the old remote behavior.
func Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "目录路径，默认为当前目录。多个独立目录可在同一 assistant 响应中并行发起 list_dir 调用。",
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
			"follow": map[string]interface{}{
				"type":        "boolean",
				"description": "是否跟随遍历中发现的符号链接目录（默认随目标平台：Windows 目标为 true，POSIX 目标为 false）。显式传入的 path 本身若为链接，会按其目标列出（与 glob/grep、rg 的显式路径语义一致）；follow 只控制该根目录之下的子链接。链接目录按链接自身路径展示，Windows junction/符号链接都会下钻，并带环路保护。",
			},
		},
		"required": []string{},
	}
}

// NewHandler builds the surface-independent list_dir Handler. The caller supplies
// the Definition (schema, description, aliases) and injects a Runtime per call;
// this keeps one listing implementation while local/remote surfaces keep their
// own schemas and output policy.
func NewHandler(def toolcore.Definition) toolcore.Handler {
	if def.Name == "" {
		def.Name = "list_dir"
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
	// A file path is a valid list_dir argument: it reports that one file's size and
	// mode instead of failing or listing its parent. The local tool has always
	// behaved this way, and `list_dir <file>` is how a caller confirms a path exists.
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
	// The junction/symlink-following default belongs to the TREE, not to this process: a
	// Windows client driving a POSIX backend must not inherit the host's habit and
	// descend links the local tool would refuse.
	follow := toolcore.TargetIsWindows(access)
	if value, set := boolArg(call.Args, "follow"); set {
		follow = value
	}

	// The root was named explicitly by the caller, so resolve it even when
	// follow=false. The flag governs links discovered below it, matching the
	// glob/grep walkers and ripgrep's treatment of an explicit path argument.
	listing, readErr := ReadTreeViaWithOptions(ctx, access, abs, depth, limit, ReadTreeOptions{
		FollowRoot:  true,
		FollowLinks: follow,
	})
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
		"follow":         follow,
		// Compatibility with the one-level listing shape.
		"entries":      listing.Total,
		"dirs":         listing.Dirs,
		"files":        listing.Files,
		"returned":     rendered,
		"is_truncated": truncated,
	}
	if truncated {
		metadata[toolresult.MetadataNextActionKey] = "目录条目超过 list_dir 预算已截断；请缩小 depth、分目录查看，或用 glob/grep 精确定位。不要原样重试同一路径。"
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

// boolArg distinguishes an explicit false from an omitted key. A non-boolean
// value is treated as false but still counts as present, matching the other
// shared handlers' tolerant argument decoding.
func boolArg(args map[string]any, key string) (bool, bool) {
	value, present := args[key]
	if !present {
		return false, false
	}
	typed, _ := value.(bool)
	return typed, true
}
