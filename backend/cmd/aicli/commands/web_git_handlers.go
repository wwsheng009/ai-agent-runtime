package commands

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
)

// 本文件为微型 Web 客户端提供「GIT」页签的后端：/web/api/git/*。
//
// 业务逻辑全部复用 internal/gitbrowse（与 runtime-server 的 /api/runtime/git/*
// 同一实现：仓库探测、状态分组、diff 解析、提交分页、stage/unstage），本层只做
// 参数解码与错误体映射。作用域根解析复用 chatWebBrowseRoots，因此「文件」页签
// 与「GIT」页签对同一会话工作目录的认知始终一致。

// gitStageBodyLimit 限制 POST /web/api/git/stage 的请求体大小：文件列表是纯文本
// 路径集合，1MiB 足够，避免超大 body 拖垮服务端（与 runtime-server 侧一致）。
const gitStageBodyLimit = 1 << 20

// HandleChatWebAPIGit 提供当前会话工作目录的 git 浏览端点族（「GIT」页签）：
//
//	GET  /web/api/git/status?scope=…&path=…                     仓库状态（分组）
//	GET  /web/api/git/diff?scope=…&path=…&file=…&target=…&context=…&whitespace=…
//	GET  /web/api/git/commits?scope=…&path=…&limit=…&cursor=…   提交分页
//	POST /web/api/git/stage  {"scope","path","action":"stage"|"unstage","files":[…]}
//
// 除 stage/unstage 外全部只读；写操作在非回环模式由页面注入的 fetch 包装自动
// 携带 X-AICLI-Token（与其它写端点同一鉴权路径）。
func HandleChatWebAPIGit(w http.ResponseWriter, r *http.Request) {
	_, service := chatWebBrowseServices()
	if service == nil {
		chatWebGitError(w, &gitbrowse.Error{
			Code:    gitbrowse.CodeGitUnavailable,
			Message: "git browse service is not configured",
			Status:  http.StatusServiceUnavailable,
		})
		return
	}
	ctx := chatWebBrowseContext(r)
	sub := strings.Trim(strings.TrimPrefix(r.URL.Path, ChatWebAPIGitPath), "/")
	query := r.URL.Query()

	switch sub {
	case "status":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Status(ctx, gitbrowse.RepoRequest{
			Scope: query.Get("scope"),
			Path:  query.Get("path"),
		})
		if err != nil {
			chatWebGitError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "diff":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Diff(ctx, gitbrowse.DiffRequest{
			RepoRequest: gitbrowse.RepoRequest{
				Scope: query.Get("scope"),
				Path:  query.Get("path"),
			},
			File:       query.Get("file"),
			Target:     query.Get("target"),
			Context:    chatWebQueryInt(query.Get("context")),
			Whitespace: query.Get("whitespace"),
		})
		if err != nil {
			chatWebGitError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "commits":
		if !chatWebRequireMethod(w, r, http.MethodGet) {
			return
		}
		result, err := service.Commits(ctx, gitbrowse.CommitsRequest{
			RepoRequest: gitbrowse.RepoRequest{
				Scope: query.Get("scope"),
				Path:  query.Get("path"),
			},
			Limit:  chatWebQueryInt(query.Get("limit")),
			Cursor: query.Get("cursor"),
		})
		if err != nil {
			chatWebGitError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	case "stage":
		if !chatWebRequireMethod(w, r, http.MethodPost) {
			return
		}
		var payload struct {
			Scope  string   `json:"scope"`
			Path   string   `json:"path"`
			Action string   `json:"action"`
			Files  []string `json:"files"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, gitStageBodyLimit))
		if err := decoder.Decode(&payload); err != nil {
			chatWebGitError(w, &gitbrowse.Error{
				Code:    gitbrowse.CodeInvalidRequest,
				Message: "invalid git stage payload: " + err.Error(),
				Status:  http.StatusBadRequest,
			})
			return
		}
		result, err := service.Stage(ctx, gitbrowse.StageRequest{
			RepoRequest: gitbrowse.RepoRequest{Scope: payload.Scope, Path: payload.Path},
			Action:      payload.Action,
			Files:       payload.Files,
		})
		if err != nil {
			chatWebGitError(w, err)
			return
		}
		writeWebAPIJSON(w, http.StatusOK, result)
	default:
		chatWebBrowseError(w, http.StatusNotFound, "git_endpoint_not_found", "unknown git endpoint: "+sub, nil)
	}
}

// chatWebGitError 输出 git 统一错误体：
//
//	{"error":{"code":"…","message":"…","exit_code":128}}
//
// exit_code 仅对 git_failed 出现（与 runtime-server 侧 gitWriteError 同形），
// 前端据 code 决定重试/降级，据 exit_code 展示底层 git 失败细节。
func chatWebGitError(w http.ResponseWriter, err error) {
	status := gitbrowse.HTTPStatus(err)
	if status < http.StatusBadRequest || status >= 600 {
		status = http.StatusInternalServerError
	}
	payload := map[string]interface{}{
		"code":    gitbrowse.Code(err),
		"message": err.Error(),
	}
	if exitCode := gitbrowse.ExitCode(err); exitCode != 0 {
		payload["exit_code"] = exitCode
	}
	writeWebAPIJSON(w, status, map[string]interface{}{"error": payload})
}
