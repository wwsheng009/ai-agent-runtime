package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// ============================================================================
// 本地 Runtime Observation Plane HTTP handler。
//
// 这些 handler 由 cmd/aicli main 包挂载到 pprof loopback HTTP 服务器上
// （路径前缀 /api/runtime/observe/v1），使 aicli 本地 in-process 模式也能提供
// 与 runtime-server 相同的 observe 端点（capabilities/snapshot/sessions/events）。
//
// 与服务端 observe_handlers.go 保持同一 envelope 与错误码契约：
//   - 响应统一使用 runtimeobserve.Envelope（ok/schema_version/request_id/data/
//     warnings/redaction）；
//   - 错误映射 observeHTTPStatus：disabled/unauthorized→403、session not
//     found→404、cursor expired→410、invalid→400、resource exceeded→429。
//
// 鉴权：这些端点只监听 127.0.0.1 loopback，等价于服务端 authorizeUsageAdmin
// 对 loopback 请求的放行，无需额外 token。
// ============================================================================

// ChatDebugObservePrefix 返回本地 observe 端点的版本化路由前缀。
func ChatDebugObservePrefix() string {
	return runtimeobserve.DefaultConfig().RoutePrefix
}

// HandleChatDebugObserveRequest 处理本地 observe 端点请求。挂载时使用前缀模式：
//
//	mux.HandleFunc(commands.ChatDebugObservePrefix()+"/", commands.HandleChatDebugObserveRequest)
//
// 请求路径按前缀 + 子路径分派：
//
//	GET /api/runtime/observe/v1/capabilities
//	GET /api/runtime/observe/v1/snapshot?include_sessions=0|1
//	GET /api/runtime/observe/v1/sessions/{session_id}
//	GET /api/runtime/observe/v1/events?...
func HandleChatDebugObserveRequest(w http.ResponseWriter, r *http.Request) {
	if w == nil || r == nil {
		return
	}
	svc := chatLocalObserveService()
	if svc == nil {
		chatObserveWriteError(w, r, http.StatusForbidden, fmt.Errorf("%s: runtime observation disabled", runtimeobserve.ErrCodeDisabled))
		return
	}
	prefix := strings.TrimRight(ChatDebugObservePrefix(), "/")
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.Trim(path, "/")

	switch {
	case path == "capabilities":
		chatObserveWriteJSON(w, r, svc.Capabilities(), nil)
	case path == "snapshot":
		handleLocalObserveSnapshot(w, r, svc)
	case strings.HasPrefix(path, "sessions/"):
		handleLocalObserveSession(w, r, svc, strings.TrimPrefix(path, "sessions/"))
	case path == "events":
		handleLocalObserveEvents(w, r, svc)
	default:
		http.NotFound(w, r)
	}
}

func handleLocalObserveSnapshot(w http.ResponseWriter, r *http.Request, svc *runtimeobserve.Service) {
	includeSessions := true
	if v := strings.TrimSpace(r.URL.Query().Get("include_sessions")); v != "" {
		includeSessions = v != "0" && v != "false"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := svc.BuildSnapshot(ctx, includeSessions)
	if err != nil {
		chatObserveWriteError(w, r, chatObserveHTTPStatus(err), err)
		return
	}
	chatObserveWriteJSON(w, r, snapshot, nil)
}

func handleLocalObserveSession(w http.ResponseWriter, r *http.Request, svc *runtimeobserve.Service, sessionID string) {
	sessionID = runtimechat.NormalizeSessionID(sessionID)
	if sessionID == "" {
		chatObserveWriteError(w, r, http.StatusBadRequest, fmt.Errorf("%s: session_id required", runtimeobserve.ErrCodeInvalidRequest))
		return
	}
	summary, err := svc.SessionFor(r.Context(), sessionID)
	if err != nil {
		chatObserveWriteError(w, r, chatObserveHTTPStatus(err), err)
		return
	}
	chatObserveWriteJSON(w, r, summary, nil)
}

func handleLocalObserveEvents(w http.ResponseWriter, r *http.Request, svc *runtimeobserve.Service) {
	q, err := chatParseObserveEventQuery(r)
	if err != nil {
		chatObserveWriteError(w, r, http.StatusBadRequest, err)
		return
	}
	result, err := svc.QueryEvents(q)
	if err != nil {
		chatObserveWriteError(w, r, chatObserveHTTPStatus(err), err)
		return
	}
	chatObserveWriteJSON(w, r, result, nil)
}

// chatObserveWriteJSON 输出统一 envelope；Data 为已投影的低敏载荷。
func chatObserveWriteJSON(w http.ResponseWriter, r *http.Request, data interface{}, warnings []string) {
	if w == nil {
		return
	}
	env := runtimeobserve.Envelope{
		OK:            true,
		SchemaVersion: runtimeobserve.SchemaVersionResponse,
		RequestID:     strings.TrimSpace(r.Header.Get("X-Request-ID")),
		Data:          data,
		Warnings:      warnings,
	}
	if svc := chatLocalObserveService(); svc != nil {
		caps := svc.Capabilities()
		env.Redaction = &caps.Redaction
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(env)
}

// chatObserveWriteError 输出错误 envelope（只带稳定错误码，不回传原始 Go error）。
func chatObserveWriteError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if w == nil {
		return
	}
	code := runtimeobserve.ErrCodeInternal
	msg := ""
	if err != nil {
		code = chatObserveErrorCode(err)
		msg = err.Error()
	}
	env := runtimeobserve.Envelope{
		OK:            false,
		SchemaVersion: runtimeobserve.SchemaVersionResponse,
		RequestID:     strings.TrimSpace(r.Header.Get("X-Request-ID")),
		Error: &runtimeobserve.ErrorBody{
			Code:    code,
			Message: msg,
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// chatObserveErrorCode 从错误消息提取稳定错误码。
func chatObserveErrorCode(err error) string {
	if err == nil {
		return runtimeobserve.ErrCodeInternal
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, runtimeobserve.ErrCodeUnauthorized):
		return runtimeobserve.ErrCodeUnauthorized
	case strings.Contains(msg, runtimeobserve.ErrCodeDisabled):
		return runtimeobserve.ErrCodeDisabled
	case strings.Contains(msg, runtimeobserve.ErrCodeSessionNotFound):
		return runtimeobserve.ErrCodeSessionNotFound
	case strings.Contains(msg, runtimeobserve.ErrCodeCursorExpired):
		return runtimeobserve.ErrCodeCursorExpired
	case strings.Contains(msg, runtimeobserve.ErrCodeCursorInvalid),
		strings.Contains(msg, runtimeobserve.ErrCodeInvalidRequest):
		return runtimeobserve.ErrCodeInvalidRequest
	case strings.Contains(msg, runtimeobserve.ErrCodeTooManyClients),
		strings.Contains(msg, runtimeobserve.ErrCodeResourceExceeded):
		return runtimeobserve.ErrCodeResourceExceeded
	default:
		return runtimeobserve.ErrCodeInternal
	}
}

// chatObserveHTTPStatus 把服务错误码映射为 HTTP 状态码。
func chatObserveHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, runtimeobserve.ErrCodeUnauthorized),
		strings.Contains(msg, runtimeobserve.ErrCodeDisabled):
		return http.StatusForbidden
	case strings.Contains(msg, runtimeobserve.ErrCodeSessionNotFound):
		return http.StatusNotFound
	case strings.Contains(msg, runtimeobserve.ErrCodeCursorExpired):
		return http.StatusGone
	case strings.Contains(msg, runtimeobserve.ErrCodeCursorInvalid),
		strings.Contains(msg, runtimeobserve.ErrCodeInvalidRequest):
		return http.StatusBadRequest
	case strings.Contains(msg, runtimeobserve.ErrCodeTooManyClients),
		strings.Contains(msg, runtimeobserve.ErrCodeResourceExceeded):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// chatParseObserveEventQuery 从 query 参数构造低敏事件查询条件。
func chatParseObserveEventQuery(r *http.Request) (runtimeobserve.EventQuery, error) {
	var q runtimeobserve.EventQuery
	values := r.URL.Query()
	q.SessionID = runtimechat.NormalizeSessionID(values.Get("session_id"))
	q.TraceID = strings.TrimSpace(values.Get("trace_id"))
	q.AgentID = strings.TrimSpace(values.Get("agent_id"))
	q.TurnID = strings.TrimSpace(values.Get("turn_id"))
	q.Provider = strings.TrimSpace(values.Get("provider"))
	q.Model = strings.TrimSpace(values.Get("model"))
	q.EventType = strings.TrimSpace(values.Get("event_type"))
	q.Source = strings.TrimSpace(values.Get("source"))

	var err error
	if v := strings.TrimSpace(values.Get("after_seq")); v != "" {
		if q.AfterSeq, err = strconv.ParseInt(v, 10, 64); err != nil {
			return q, fmt.Errorf("%s: after_seq must be an integer", runtimeobserve.ErrCodeInvalidRequest)
		}
	}
	if v := strings.TrimSpace(values.Get("before_seq")); v != "" {
		if q.BeforeSeq, err = strconv.ParseInt(v, 10, 64); err != nil {
			return q, fmt.Errorf("%s: before_seq must be an integer", runtimeobserve.ErrCodeInvalidRequest)
		}
	}
	if v := strings.TrimSpace(values.Get("since")); v != "" {
		if t, perr := time.Parse(time.RFC3339, v); perr != nil {
			return q, fmt.Errorf("%s: since must be RFC3339", runtimeobserve.ErrCodeInvalidRequest)
		} else {
			q.Since = &t
		}
	}
	if v := strings.TrimSpace(values.Get("until")); v != "" {
		if t, perr := time.Parse(time.RFC3339, v); perr != nil {
			return q, fmt.Errorf("%s: until must be RFC3339", runtimeobserve.ErrCodeInvalidRequest)
		} else {
			q.Until = &t
		}
	}
	if v := strings.TrimSpace(values.Get("limit")); v != "" {
		if q.Limit, err = strconv.Atoi(v); err != nil || q.Limit < 0 {
			return q, fmt.Errorf("%s: limit must be a non-negative integer", runtimeobserve.ErrCodeInvalidRequest)
		}
	}
	return q, nil
}
