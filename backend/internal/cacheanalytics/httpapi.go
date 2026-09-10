package cacheanalytics

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler 返回缓存分析 API 的独立 http.Handler（供宿主自行注册子树）。
func Handler(prefix string, src Source) http.Handler {
	if src == nil || prefix == "" {
		return nil
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCacheAPI(w, r, prefix, src)
	})
}

// Mount 把缓存分析 HTTP 契约挂载到 mux（只定义一次，§6.3）。
//
// 路由（prefix 形如 /web/api/cache 或 /api/runtime/sessions/{id}/cache）：
//
//	GET {prefix}/capabilities                    能力发现
//	GET {prefix}/overview?session_id=            会话总览
//	GET {prefix}/requests?session_id=&...        明细分页（trace_id/turn_id/message_id/
//	                                             status/cache_status/from/to/limit/offset）
//	GET {prefix}/requests/{llm_request_id}       单请求详情
//	GET {prefix}/messages/{message_id}/trace     消息追溯
//
// 错误 envelope：{"error":{"code":"...","message":"..."}}，稳定错误码见 types.go。
// 不使用 Go1.22 路径通配符（win7 构建兼容），子路径手动解析。
func Mount(mux *http.ServeMux, prefix string, src Source) {
	if mux == nil || src == nil || prefix == "" {
		return
	}
	handler := Handler(prefix, src)
	if handler == nil {
		return
	}
	mux.Handle(prefix, handler)
	mux.Handle(prefix+"/", handler)
}

func serveCacheAPI(w http.ResponseWriter, r *http.Request, prefix string, src Source) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, ErrInvalidRequest, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	rest = strings.TrimPrefix(rest, "/")
	segments := splitPath(rest)
	switch {
	case len(segments) == 1 && segments[0] == "capabilities":
		writeJSON(w, http.StatusOK, src.Capabilities())
	case len(segments) == 1 && segments[0] == "overview":
		sessionID := r.URL.Query().Get("session_id")
		overview, err := src.Overview(sessionID)
		if err != nil {
			writeSourceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, overview)
	case len(segments) == 1 && segments[0] == "requests":
		query, err := parseRequestQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrInvalidRequest, err.Error())
			return
		}
		response, err := src.Requests(r.URL.Query().Get("session_id"), query)
		if err != nil {
			writeSourceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case len(segments) == 2 && segments[0] == "requests":
		record, err := src.Request(r.URL.Query().Get("session_id"), segments[1])
		if err != nil {
			writeSourceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, record)
	case len(segments) == 3 && segments[0] == "messages" && segments[2] == "trace":
		trace, err := src.MessageTrace(r.URL.Query().Get("session_id"), segments[1])
		if err != nil {
			writeSourceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, trace)
	default:
		writeError(w, http.StatusNotFound, ErrNotFound, "unknown cache endpoint")
	}
}

func splitPath(rest string) []string {
	if rest == "" {
		return nil
	}
	parts := strings.Split(rest, "/")
	for i, part := range parts {
		parts[i] = strings.TrimPrefix(part, "/")
	}
	return parts
}

func parseRequestQuery(r *http.Request) (RequestQuery, error) {
	params := r.URL.Query()
	query := RequestQuery{
		TraceID:     strings.TrimSpace(params.Get("trace_id")),
		TurnID:      strings.TrimSpace(params.Get("turn_id")),
		MessageID:   strings.TrimSpace(params.Get("message_id")),
		Status:      strings.TrimSpace(params.Get("status")),
		CacheStatus: strings.TrimSpace(params.Get("cache_status")),
	}
	if raw := strings.TrimSpace(params.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return RequestQuery{}, errInvalid("limit")
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(params.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return RequestQuery{}, errInvalid("offset")
		}
		query.Offset = offset
	}
	if raw := strings.TrimSpace(params.Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return RequestQuery{}, errInvalid("from")
		}
		query.From = &parsed
	}
	if raw := strings.TrimSpace(params.Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return RequestQuery{}, errInvalid("to")
		}
		query.To = &parsed
	}
	return query, nil
}

func errInvalid(field string) error {
	return &fieldError{field: field}
}

type fieldError struct{ field string }

func (e *fieldError) Error() string { return "invalid query parameter: " + e.field }

func writeSourceError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, struct{}{})
	case err == ErrInvalidRequest:
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid request")
	case err == ErrSessionNotFound:
		writeError(w, http.StatusNotFound, ErrSessionNotFound, "session not found")
	case err == ErrNotFound:
		writeError(w, http.StatusNotFound, ErrNotFound, "not found")
	case err == ErrDisabled:
		writeError(w, http.StatusServiceUnavailable, ErrDisabled, "cache analytics disabled")
	default:
		writeError(w, http.StatusInternalServerError, ErrInternal, "internal error")
	}
}

func writeError(w http.ResponseWriter, status int, code error, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code.Error(),
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}
