package skills

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
)

// gitStageBodyLimit 限制 POST /git/stage 请求体大小：文件列表是纯文本路径集合，
// 1MiB 足够，避免超大 body 拖垮服务端。
const gitStageBodyLimit = 1 << 20

// gitHandlers 承载 /git/* 的 HTTP 细节：参数解码、错误体、状态码映射。
// 所有命令都传 r.Context()，客户端断开即 kill 子进程（§5.8）。
type gitHandlers struct {
	service GitBrowseService
}

// status 处理 GET /git/status?scope=…&path=…
func (h *gitHandlers) status(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Status(r.Context(), gitbrowse.RepoRequest{
		Scope: gitDecodeQuery(query.Get("scope")),
		Path:  gitDecodeQuery(query.Get("path")),
	})
	if err != nil {
		gitWriteError(w, r, err)
		return
	}
	gitWriteJSON(w, http.StatusOK, result)
}

// diff 处理 GET /git/diff?scope=…&path=…&file=…&target=…&context=…&whitespace=…
func (h *gitHandlers) diff(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Diff(r.Context(), gitbrowse.DiffRequest{
		RepoRequest: gitbrowse.RepoRequest{
			Scope: gitDecodeQuery(query.Get("scope")),
			Path:  gitDecodeQuery(query.Get("path")),
		},
		File:       gitDecodeQuery(query.Get("file")),
		Target:     gitDecodeQuery(query.Get("target")),
		Context:    gitQueryInt(query.Get("context")),
		Whitespace: gitDecodeQuery(query.Get("whitespace")),
	})
	if err != nil {
		gitWriteError(w, r, err)
		return
	}
	gitWriteJSON(w, http.StatusOK, result)
}

// commits 处理 GET /git/commits?scope=…&path=…&limit=…&cursor=…
func (h *gitHandlers) commits(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Commits(r.Context(), gitbrowse.CommitsRequest{
		RepoRequest: gitbrowse.RepoRequest{
			Scope: gitDecodeQuery(query.Get("scope")),
			Path:  gitDecodeQuery(query.Get("path")),
		},
		Limit:  gitQueryInt(query.Get("limit")),
		Cursor: gitDecodeQuery(query.Get("cursor")),
	})
	if err != nil {
		gitWriteError(w, r, err)
		return
	}
	gitWriteJSON(w, http.StatusOK, result)
}

// stage 处理 POST /git/stage：{"scope","path","action":"stage"|"unstage","files":[...]}
// 请求体只解析为本地私有结构，避免在 skills 包内引入额外导出类型。
func (h *gitHandlers) stage(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
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
		gitWriteError(w, r, gitRequestError("invalid git stage payload: "+err.Error()))
		return
	}
	result, err := h.service.Stage(r.Context(), gitbrowse.StageRequest{
		RepoRequest: gitbrowse.RepoRequest{Scope: payload.Scope, Path: payload.Path},
		Action:      payload.Action,
		Files:       payload.Files,
	})
	if err != nil {
		gitWriteError(w, r, err)
		return
	}
	gitWriteJSON(w, http.StatusOK, result)
}

// requireService 在服务未注入时统一返回 503 git_unavailable（不 panic、不 500）。
func (h *gitHandlers) requireService(w http.ResponseWriter, r *http.Request) bool {
	if h != nil && h.service != nil {
		return true
	}
	gitWriteError(w, r, &gitbrowse.Error{
		Code:    gitbrowse.CodeGitUnavailable,
		Message: "git browse service is not configured",
		Status:  http.StatusServiceUnavailable,
	})
	return false
}

// gitRequestError 构造请求级 400 错误（复用 gitbrowse 的错误码与状态码表）。
func gitRequestError(message string) *gitbrowse.Error {
	return &gitbrowse.Error{
		Code:    gitbrowse.CodeInvalidRequest,
		Message: message,
		Status:  http.StatusBadRequest,
	}
}

// gitDecodeQuery 做一次补充的 percent 解码（§5.2：先解码、再 Clean、再校验，顺序不能颠倒）。
// 查询串已被 net/http 解码过一层，这里处理二次编码（%2e%2e）的绕过尝试；
// 解码失败（如文件名含裸 %）时保留原值，由 gitbrowse 的作用域校验兜底。
func gitDecodeQuery(value string) string {
	if value == "" || !strings.Contains(value, "%") {
		return value
	}
	if decoded, err := url.QueryUnescape(value); err == nil {
		return decoded
	}
	return value
}

// gitQueryInt 解析整数查询参数；缺失或非法返回 0，由服务层套用默认值与上限。
func gitQueryInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

// gitWriteJSON 输出 JSON 响应。
func gitWriteJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

// gitWriteError 输出统一错误体（§5.7）：
//
//	{ "error": { "code": "…", "message": "…", "exit_code": 128 }, "request_id": "…" }
//
// exit_code 仅对 git_failed 出现；request_id 始终存在（未知时为空串）。
func gitWriteError(w http.ResponseWriter, r *http.Request, err error) {
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
	gitWriteJSON(w, status, map[string]interface{}{
		"error":      payload,
		"request_id": gitRequestID(w, r),
	})
}

// gitRequestID 取请求 ID：优先用中间件写在响应头上的值，其次回退请求头（复用既有 helper）。
func gitRequestID(w http.ResponseWriter, r *http.Request) string {
	if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(requestIDFromRequest(r))
}
