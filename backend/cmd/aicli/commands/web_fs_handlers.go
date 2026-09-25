package commands

import (
	"context"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
)

// 本文件为微型 Web 客户端提供「文件」页签的后端：/web/api/fs/*。
//
// 设计要点（与 runtime-server 的 /api/runtime/fs/* 同源）：
//   - 业务逻辑一律复用 internal/filebrowse + internal/fsscope，本层只做
//     HTTP 细节（参数解码、错误体、状态码映射），不重复实现路径校验；
//   - scope/path 不在本层解码：URL 解码必须与 Clean、越界校验严格同序，
//     统一由 fsscope 负责（规划 §5.2「先解码再 Clean 再校验」）；
//   - 作用域根由 chatWebBrowseRoots 解析（当前 aicli 会话的工作目录），
//     同一个适配器同时满足 fsscope.RootResolver 与 gitbrowse.RootResolver。

// ---------------------------------------------------------------------------
// 作用域根解析：aicli 会话工作目录
// ---------------------------------------------------------------------------

// chatWebBrowseRoots 把 aicli 进程内的会话工作目录适配为 RootResolver。
// 方法集与 internal/fsscope、internal/gitbrowse 的同名接口一致，因此一个实例
// 可以同时注入两个服务，避免出现「文件页签能看到、GIT 页签看不到」的口径分裂。
//
// aicli 进程内没有独立的「工作目录注册表」（那是 runtime-server 的
// /workspace-directories），因此 WorkspaceRoot 如实返回未命中，由 fsscope
// 映射为 404 scope_not_found——绝不回退到进程 cwd，避免把无关目录列出来。
type chatWebBrowseRoots struct{}

// WorkspaceRoot 永远未命中（aicli 无工作目录注册表）。
func (chatWebBrowseRoots) WorkspaceRoot(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}

// SessionRoot 解析会话工作目录：
//  1. 会话存储中的任意会话（含非当前会话）——与 TUI /resume 的口径一致；
//  2. 当前会话自身（尚未持久化 / 存储未注入时），路径取自
//     sessionmeta.WorkspacePath，缺失时回退到本地 workspace 配置解析。
//
// 找不到工作目录时返回 ok=false（映射为 404），不猜测、不返回进程 cwd。
func (chatWebBrowseRoots) SessionRoot(ctx context.Context, sessionID string) (string, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", false, nil
	}
	current := chatWebSession()
	if current == nil {
		return "", false, nil
	}
	if current.SessionManager != nil {
		if stored, err := current.SessionManager.GetSession(ctx, sessionID); err == nil && stored != nil {
			if path := chatWebSessionWorkspacePath(stored); path != "" {
				return path, true, nil
			}
			return "", false, nil
		}
	}
	if current.RuntimeSession != nil && strings.TrimSpace(current.RuntimeSession.ID) == sessionID {
		if path := chatWebCurrentWorkspacePath(current); path != "" {
			return path, true, nil
		}
	}
	return "", false, nil
}

// chatWebCurrentWorkspacePath 取当前 aicli 会话的工作目录：先看已落到
// sessionmeta 的路径，再回退到本地 workspace 配置（与 chat 启动时同一函数）。
func chatWebCurrentWorkspacePath(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if path := runtimeSessionWorkspacePath(session.RuntimeSession); path != "" {
		return path
	}
	return strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session))
}

// ---------------------------------------------------------------------------
// 服务单例
// ---------------------------------------------------------------------------

var (
	chatWebBrowseOnce   sync.Once
	chatWebFSService    *filebrowse.Service
	chatWebGitService   *gitbrowse.Service
	chatWebBrowseShared *chatWebBrowseRoots
)

// chatWebBrowseServices 惰性构造文件 / git 浏览服务（进程内单例）。
// 单例是必要的：gitbrowse.Service 内部有仓库探测缓存（repoCache），按请求新建
// 会让每次状态刷新都重跑 git rev-parse。
func chatWebBrowseServices() (*filebrowse.Service, *gitbrowse.Service) {
	chatWebBrowseOnce.Do(func() {
		chatWebBrowseShared = &chatWebBrowseRoots{}
		chatWebFSService = filebrowse.NewService(filebrowse.Deps{Roots: chatWebBrowseShared})
		chatWebGitService = gitbrowse.NewService(gitbrowse.Deps{Roots: chatWebBrowseShared})
	})
	return chatWebFSService, chatWebGitService
}

// chatWebBrowseContext 把会话标识桥接进 ctx：fsscope 的会话根解析依赖
// WithSessionID（HTTP 层没有隐式会话上下文）。查询串缺省时回退到当前会话，
// 因为本端点本就属于这个 aicli 进程的唯一会话。
func chatWebBrowseContext(r *http.Request) context.Context {
	ctx := r.Context()
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		sessionID = chatWebCurrentSessionID()
	}
	if sessionID == "" {
		return ctx
	}
	return fsscope.WithSessionID(ctx, sessionID)
}

// chatWebCurrentSessionID 返回当前 aicli 会话 ID（无会话时为空串）。
func chatWebCurrentSessionID() string {
	session := chatWebSession()
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(session.RuntimeSession.ID)
}

// ---------------------------------------------------------------------------
// /web/api/fs/*
// ---------------------------------------------------------------------------

// HandleChatWebAPIFs 提供当前会话工作目录的文件浏览端点族（「文件」页签）：
//
//	GET /web/api/fs/roots?session_id=<id>                      作用域根列表
//	GET /web/api/fs/list?scope=…&path=…&cursor=…&limit=…&sort=…&show_hidden=…&dirs_first=…
//	GET /web/api/fs/stat?scope=…&path=…                        单路径元信息
//	GET /web/api/fs/preview?scope=…&path=…&max_bytes=…         文本/图片预览
//	GET /web/api/fs/download?scope=…&path=…                    文件下载（支持 Range）
//	GET /web/api/fs/search?scope=…&q=…&path=…&cursor=…&limit=…&kinds=…&max_depth=…
//
// 全部为只读端点（写令牌在非回环模式由页面注入的 fetch 包装统一携带）。
// 错误体与其它 web 端点一致：{"error":{"code","message",…details}}。
func HandleChatWebAPIFs(w http.ResponseWriter, r *http.Request) {
	service, _ := chatWebBrowseServices()
	if service == nil {
		chatWebBrowseError(w, http.StatusServiceUnavailable, "fs_unavailable", "file browse service is not configured", nil)
		return
	}
	ctx := chatWebBrowseContext(r)
	sub := strings.Trim(strings.TrimPrefix(r.URL.Path, ChatWebAPIFsPath), "/")
	query := r.URL.Query()

	switch sub {
	case "roots":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		roots, err := service.ListRoots(ctx)
		if err != nil {
			chatWebBrowseWriteReadError(w, err)
			return
		}
		if roots == nil {
			roots = []filebrowse.Root{} // 契约：roots 必须是数组，不能是 null
		}
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
			"roots": roots,
			"count": len(roots),
		})
	case "list":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.List(ctx, filebrowse.ListRequest{
			Scope:      query.Get("scope"),
			Path:       query.Get("path"),
			Cursor:     query.Get("cursor"),
			Limit:      chatWebQueryInt(query.Get("limit")),
			Sort:       query.Get("sort"),
			ShowHidden: chatWebQueryBool(query.Get("show_hidden")),
			DirsFirst:  chatWebQueryBool(query.Get("dirs_first")),
		})
		if err != nil {
			chatWebBrowseWriteReadError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "stat":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Stat(ctx, filebrowse.PathRequest{
			Scope: query.Get("scope"),
			Path:  query.Get("path"),
		})
		if err != nil {
			chatWebBrowseWriteReadError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "preview":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Preview(ctx, filebrowse.PreviewRequest{
			Scope:    query.Get("scope"),
			Path:     query.Get("path"),
			MaxBytes: chatWebQueryInt(query.Get("max_bytes")),
		})
		if err != nil {
			chatWebBrowseWriteReadError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "download":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			chatWebBrowseError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method "+r.Method+" is not supported", nil)
			return
		}
		chatWebFSDownload(w, r, ctx, service, query.Get("scope"), query.Get("path"))
	case "search":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Search(ctx, filebrowse.SearchRequest{
			Scope:      query.Get("scope"),
			Query:      query.Get("q"),
			Path:       query.Get("path"),
			Cursor:     query.Get("cursor"),
			Limit:      chatWebQueryInt(query.Get("limit")),
			ShowHidden: chatWebQueryBool(query.Get("show_hidden")),
			Kinds:      query.Get("kinds"),
			MaxDepth:   chatWebQueryInt(query.Get("max_depth")),
			MaxScan:    chatWebQueryInt(query.Get("max_scan")),
			BudgetMs:   chatWebQueryInt(query.Get("budget_ms")),
		})
		if err != nil {
			chatWebBrowseWriteReadError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	default:
		chatWebBrowseError(w, http.StatusNotFound, "fs_endpoint_not_found", "unknown fs endpoint: "+sub, nil)
	}
}

// chatWebFSDownload 流式下载：复用 http.ServeContent（自动处理 Range /
// If-Range / 416），文件句柄由 OpenDownload 打开并在此关闭（避免 TOCTOU）。
func chatWebFSDownload(w http.ResponseWriter, r *http.Request, ctx context.Context, service *filebrowse.Service, scope, path string) {
	target, err := service.OpenDownload(ctx, filebrowse.PathRequest{Scope: scope, Path: path})
	if err != nil {
		chatWebBrowseWriteReadError(w, err)
		return
	}
	defer func() { _ = target.File.Close() }()

	if contentType := mime.TypeByExtension(filepath.Ext(target.Name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+chatWebHeaderSafeName(target.Name)+"\"")
	// 零值 ModTime：跳过 Last-Modified，但 Range / Content-Length 仍由
	// ServeContent 依据 Seek 结果正常处理。
	http.ServeContent(w, r, target.Name, time.Time{}, target.File)
}

// chatWebHeaderSafeName 清洗下载文件名中的裸引号与换行，避免头部注入。
func chatWebHeaderSafeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\"", "'")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if name == "" {
		return "download"
	}
	return name
}

// ---------------------------------------------------------------------------
// 公共小工具（web 端点层）
// ---------------------------------------------------------------------------

// chatWebQueryInt 解析整数查询参数；缺失或非法返回 0，由服务层套用默认值与上限。
func chatWebQueryInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

// chatWebQueryBool 解析布尔查询参数（1/true/yes/on 视为真）；缺失或非法返回 false。
func chatWebQueryBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// chatWebBrowseError 写统一的 web 错误 envelope：
//
//	{"error":{"code":"…","message":"…", …details}}
func chatWebBrowseError(w http.ResponseWriter, status int, code, message string, details map[string]interface{}) {
	payload := map[string]interface{}{
		"code":    code,
		"message": message,
	}
	for key, value := range details {
		payload[key] = value
	}
	writeWebAPIJSON(w, status, map[string]interface{}{"error": payload})
}

// chatWebBrowseWriteReadError 把 filebrowse / fsscope 的错误映射为响应：
// 机器码、HTTP 状态与 details（如 expected_offset）全部来自 fsscope，本层不猜测。
func chatWebBrowseWriteReadError(w http.ResponseWriter, err error) {
	code := fsscope.CodeOf(err)
	if code == "" {
		code = "fs_error"
	}
	status := fsscope.StatusOf(err, http.StatusInternalServerError)
	chatWebBrowseError(w, status, code, err.Error(), fsscope.DetailsOf(err))
}
