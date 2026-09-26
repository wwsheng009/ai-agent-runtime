package runtimeapi

// P0-3：把「工作目录注册表」与「会话存储」适配为 fsscope.RootResolver，
// 供 /fs/* 与 /git/* 的作用域解析使用（规划 §5.2 / D3）。
//
// 纪律：
//   - 只读：不创建、不修改注册表与会话记录（改动注册表是既有 /workspace-directories 的职责）；
//   - 未命中（未注册 id、会话无绑定目录、会话存储未注入）一律返回 ok=false，
//     由 fsscope 映射为 404 scope_not_found / 400 scope_has_no_root，
//     绝不回退到进程 cwd（否则越权根会被静默放大）；
//   - 会话 id 只从 ctx 读取（fsscope.SessionIDFromContext），接口层不新增旁路参数。

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// FSBrowserRoots 返回文件 / Git 面板的作用域解析器。
//
// Handler 为 nil 或会话存储未注入时返回 nil：调用方（runtime-server）应据此
// 跳过服务注入，路由保持「注册但 503 降级」的既有口径，而不是 panic。
func (h *Handler) FSBrowserRoots() fsscope.RootResolver {
	if h == nil {
		return nil
	}
	return &fsBrowserRoots{handler: h}
}

type fsBrowserRoots struct {
	handler *Handler
}

// WorkspaceRoot 按注册目录 id 解析绝对路径。
func (r *fsBrowserRoots) WorkspaceRoot(_ context.Context, id string) (string, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false, nil
	}
	store := r.handler.workspaceDirectoryRegistry()
	if store == nil {
		return "", false, nil
	}
	record, ok := store.Get(id)
	if !ok || strings.TrimSpace(record.Path) == "" {
		return "", false, nil
	}
	return record.Path, true, nil
}

// ListWorkspaceRoots 枚举已注册工作目录（RootLister 扩展；注册表缺失时返回空表，
// 由 /fs/roots 继续提供会话根与进程 cwd，而不是整体失败）。
func (r *fsBrowserRoots) ListWorkspaceRoots(_ context.Context) ([]fsscope.WorkspaceRoot, error) {
	store := r.handler.workspaceDirectoryRegistry()
	if store == nil {
		return nil, nil
	}
	records := store.List()
	roots := make([]fsscope.WorkspaceRoot, 0, len(records))
	for _, record := range records {
		path := strings.TrimSpace(record.Path)
		if path == "" {
			continue
		}
		roots = append(roots, fsscope.WorkspaceRoot{
			ID:   strings.TrimSpace(record.ID),
			Path: path,
			Name: strings.TrimSpace(record.Name),
		})
	}
	return roots, nil
}

// SessionRoot 取会话创建时绑定的工作目录（metadata.context[workspace_path]）。
//
// 未绑定目录的会话返回 ok=false —— 「没有工作目录」与「目录不可用」必须可区分，
// 后者由上层在 stat/浏览时以 fs_read_failed 如实上报。
func (r *fsBrowserRoots) SessionRoot(ctx context.Context, sessionID string) (string, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || r.handler.sessionManager == nil {
		return "", false, nil
	}
	session, err := r.handler.sessionManager.GetSession(ctx, sessionID)
	if err != nil || session == nil {
		return "", false, nil
	}
	if session.Metadata.Context == nil {
		return "", false, nil
	}
	value, ok := session.Metadata.Context[sessionmeta.WorkspacePath].(string)
	if !ok {
		return "", false, nil
	}
	path := strings.TrimSpace(value)
	if path == "" {
		return "", false, nil
	}
	return path, true, nil
}
