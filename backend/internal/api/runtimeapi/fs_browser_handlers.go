package runtimeapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// fsBrowserHandlers 承载 /fs/* 的 HTTP 细节：参数解码、错误体、状态码映射。
// 所有 handler 都传 r.Context()：客户端断开（关抽屉/切目录）即刻取消在途读取。
type fsBrowserHandlers struct {
	service FSBrowserService
}

// roots 处理 GET /fs/roots：返回可用作用域根（注册工作目录 + 会话根 + cwd）。
//
// 会话根要求调用方显式带 `session_id`：HTTP 层没有隐式会话上下文，也不能从其它头
// 里猜（猜错等于把别的工作目录列出来），因此只在显式参数存在时桥接。
func (h *fsBrowserHandlers) roots(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	roots, err := h.service.ListRoots(fsRequestContext(r))
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	if roots == nil {
		roots = []filebrowse.Root{} // 契约：roots 必须是数组，不能是 null
	}
	fsWriteJSON(w, http.StatusOK, map[string]interface{}{"roots": roots, "count": len(roots)})
}

// fsRequestContext 把可选的 session_id 桥接进 request context。
//
// 只有 /fs/roots 需要它：其余端点的作用域已自带 `session:<id>`（D3），
// 而「列出可用根」本身没有 scope 参数，需要一个会话身份才能附上会话工作目录。
func fsRequestContext(r *http.Request) context.Context {
	ctx := r.Context()
	query := r.URL.Query()
	sessionID := strings.TrimSpace(query.Get("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(query.Get("sessionId"))
	}
	if sessionID == "" {
		return ctx
	}
	return fsscope.WithSessionID(ctx, sessionID)
}

// list 处理 GET /fs/list?scope=…&path=…&cursor=…&limit=…&sort=…&show_hidden=…&dirs_first=…
func (h *fsBrowserHandlers) list(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.List(r.Context(), filebrowse.ListRequest{
		// scope/path 不在这里解码：URL 解码必须与 Clean、越界校验严格同序，
		// 统一由 fsscope 负责（规划 §5.2「先解码再 Clean 再校验」）。
		Scope:      query.Get("scope"),
		Path:       query.Get("path"),
		Cursor:     query.Get("cursor"),
		Limit:      fsQueryInt(query.Get("limit")),
		Sort:       query.Get("sort"),
		ShowHidden: fsQueryBool(query.Get("show_hidden")),
		DirsFirst:  fsQueryBool(query.Get("dirs_first")),
	})
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// search 处理 GET /fs/search?scope=…&q=…&path=…&cursor=…&limit=…&show_hidden=…&kinds=…&max_depth=…&max_scan=…&budget_ms=…
//
// 只做参数解码与错误渲染：限额夹紧、字面匹配、扫描上限与游标校验都在 filebrowse.Service
// （规划 §4.4.5「服务端夹紧」）；错误统一走 fsWriteReadError 的既有错误体。
func (h *fsBrowserHandlers) search(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Search(r.Context(), filebrowse.SearchRequest{
		// scope/path 不在这里解码：URL 解码必须与 Clean、越界校验严格同序（同 /fs/list）。
		Scope:      query.Get("scope"),
		Query:      query.Get("q"),
		Path:       query.Get("path"),
		Cursor:     query.Get("cursor"),
		Limit:      fsQueryInt(query.Get("limit")),
		ShowHidden: fsQueryBool(query.Get("show_hidden")),
		Kinds:      query.Get("kinds"),
		MaxDepth:   fsQueryInt(query.Get("max_depth")),
		MaxScan:    fsQueryInt(query.Get("max_scan")),
		BudgetMs:   fsQueryInt(query.Get("budget_ms")),
	})
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// stat 处理 GET /fs/stat?scope=…&path=…：单路径元信息。
func (h *fsBrowserHandlers) stat(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Stat(r.Context(), filebrowse.PathRequest{
		Scope: query.Get("scope"),
		Path:  query.Get("path"),
	})
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// preview 处理 GET /fs/preview?scope=…&path=…&max_bytes=…&encoding=…
// encoding 目前只支持 utf-8（服务端不转码），未知值按默认处理。
func (h *fsBrowserHandlers) preview(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	result, err := h.service.Preview(r.Context(), filebrowse.PreviewRequest{
		Scope:    query.Get("scope"),
		Path:     query.Get("path"),
		MaxBytes: fsQueryInt(query.Get("max_bytes")),
	})
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// download 处理 GET/HEAD /fs/download?scope=…&path=…：流式下载，支持单区间 Range。
//
// 复用 http.ServeContent（自动处理 Range/If-Range/Last-Modified/416）；多区间不支持，
// 显式返回 416 + Content-Range: bytes */size（规划 §5.4）。
func (h *fsBrowserHandlers) download(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	query := r.URL.Query()
	target, err := h.service.OpenDownload(r.Context(), filebrowse.PathRequest{
		Scope: query.Get("scope"),
		Path:  query.Get("path"),
	})
	if err != nil {
		fsWriteReadError(w, r, err)
		return
	}
	defer target.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	if strings.Contains(r.Header.Get("Range"), ",") {
		// 多区间：不提供 multipart/byteranges，让前端退回单区间重试。
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", target.Size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Type", target.ContentType)
	w.Header().Set("ETag", target.ETag())
	w.Header().Set("Content-Disposition", fsContentDisposition(target.Name))
	http.ServeContent(w, r, target.Name, target.ModTime, target.File)
}

// requireService 在服务未注入时统一返回 503 service_unavailable（不 panic、不 500）。
func (h *fsBrowserHandlers) requireService(w http.ResponseWriter, r *http.Request) bool {
	if h != nil && h.service != nil {
		return true
	}
	fsWriteJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    fsscope.CodeServiceUnavailable,
			"message": "file browser service is not configured",
		},
		"request_id": fsRequestID(w, r),
	})
	return false
}

// fsWriteReadError 渲染读类端点的错误体（回退码 fs_read_failed）。
func fsWriteReadError(w http.ResponseWriter, r *http.Request, err error) {
	fsWriteError(w, r, err, fsscope.CodeFSReadFailed)
}

// fsWriteError 渲染统一错误体（规划 §5.7）：
//
//	{ "error": { "code": "…", "message": "…" }, "request_id": "…" }
//
// 服务层附加的上下文字段（expected_offset / target）按 §5.4 示例平铺在顶层，
// 否则前端只能靠解析 message 才能拿到真实 offset。
func fsWriteError(w http.ResponseWriter, r *http.Request, err error, fallbackCode string) {
	status := http.StatusInternalServerError
	code := fallbackCode
	message := "request failed"
	details := map[string]interface{}{}
	if err != nil {
		message = err.Error()
	}
	if target, ok := fsscope.AsError(err); ok {
		if target.HTTPStatus > 0 {
			status = target.HTTPStatus
		}
		if target.Code != "" {
			code = target.Code
		}
		if target.Message != "" {
			message = target.Message
		}
		for key, value := range target.Details {
			details[key] = value
		}
	}
	if code == "" {
		code = fallbackCode
	}
	payload := map[string]interface{}{"error": map[string]interface{}{"code": code, "message": message}, "request_id": fsRequestID(w, r)}
	for key, value := range details {
		payload[key] = value
	}
	fsWriteJSON(w, status, payload)
}

// fsWriteJSON 输出 JSON 响应。
func fsWriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// fsRequestID 取请求 ID：优先用中间件写在响应头上的值，其次回退请求头；
// 都没有时生成一个，保证错误体里 request_id 字段始终存在（便于对日志）。
func fsRequestID(w http.ResponseWriter, r *http.Request) string {
	if value := strings.TrimSpace(w.Header().Get("X-Request-ID")); value != "" {
		return value
	}
	if value := strings.TrimSpace(requestIDFromRequest(r)); value != "" {
		return value
	}
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return ""
	}
	return "req_" + hex.EncodeToString(buffer)
}

func fsQueryInt(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func fsQueryBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// fsContentDisposition 生成 `attachment; filename*=UTF-8”…`（RFC 5987）；
// 纯 ASCII 文件名额外给出 filename="…" 兜底，兼容不认 filename* 的老客户端。
func fsContentDisposition(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "download"
	}
	parts := make([]string, 0, 2)
	if ascii := fsASCIIFilename(trimmed); ascii != "" {
		parts = append(parts, fmt.Sprintf(`filename="%s"`, ascii))
	}
	parts = append(parts, "filename*=UTF-8''"+fsPercentEncode(trimmed))
	return "attachment; " + strings.Join(parts, "; ")
}

// fsASCIIFilename 返回可安全放进引号内的 ASCII 文件名；含非 ASCII 或引号/反斜杠时返回空串。
func fsASCIIFilename(name string) string {
	for _, char := range name {
		if char > 127 || char == '"' || char == '\\' || char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return name
}

// fsPercentEncode 按 RFC 5987 的 attr-char 集做百分号编码。
func fsPercentEncode(value string) string {
	const upperHex = "0123456789ABCDEF"
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if fsAttrChar(char) {
			builder.WriteByte(char)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(upperHex[char>>4])
		builder.WriteByte(upperHex[char&0x0f])
	}
	return builder.String()
}

func fsAttrChar(char byte) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return true
	}
	switch char {
	case '!', '#', '$', '&', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
