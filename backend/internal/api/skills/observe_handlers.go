package skills

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// Runtime Observation Plane (Phase 0–2) HTTP handlers.
//
// 所有接口只读、低敏、版本化：数据先经过 projector/redactor 的 allowlist +
// deny-first 双保险，再进入响应。SSE(/stream) 与 renderer 观测依赖 Phase 3/4，
// v1 阶段不注册对应路由。启用开关 runtime.observe.enabled，默认关闭。

// ensureObserveService 惰性构建观察服务；enabled=false 或配置缺失时返回 nil。
// 由于订阅了 runtime event bus 并启动 collector 循环，仅在需要时创建一次。
func (h *Handler) ensureObserveService() *runtimeobserve.Service {
	if h == nil {
		return nil
	}
	h.observeMu.RLock()
	svc := h.observeService
	h.observeMu.RUnlock()
	if svc != nil {
		return svc
	}

	h.observeMu.Lock()
	defer h.observeMu.Unlock()
	if h.observeService != nil {
		return h.observeService
	}
	if h.runtimeConfig == nil || !h.runtimeConfig.Observe.Enabled {
		return nil
	}

	cfg := runtimeobserve.WithDefaults(h.runtimeConfig.Observe)
	redactor := runtimeobserve.NewRedactor(nil, "", cfg.RedactionProfile)
	projector := runtimeobserve.NewProjector(redactor, cfg.ExposeProviderRequestID, cfg.MaxEventBytes)
	collector := runtimeobserve.NewCollector(cfg, h.getRuntimeEventBus(), projector)
	if collector == nil {
		return nil
	}
	collector.Start()
	svc = runtimeobserve.NewService(cfg, collector, redactor, nil, &observeSessionSource{h: h})
	h.observeService = svc
	return svc
}

// observeSessionSource 把活动 session actor 的最低限度摘要投影为观测 SessionSummary。
// 只读取 StateSummary()（idle/running 等状态 + turn id），不触碰 prompt、工具参数、
// tool receipt、checkpoint 内容等敏感或重量级数据。
type observeSessionSource struct {
	h *Handler
}

func (o *observeSessionSource) hub() *chat.SessionHub {
	if o == nil || o.h == nil {
		return nil
	}
	return o.h.getSessionHub()
}

func (o *observeSessionSource) ObservationSessionSummaries(ctx context.Context, limit int) ([]runtimeobserve.SessionSummary, error) {
	hub := o.hub()
	if hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	ids := hub.ActiveSessionIDs(limit)
	out := make([]runtimeobserve.SessionSummary, 0, len(ids))
	for _, id := range ids {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			default:
			}
		}
		actor, ok := hub.Get(id)
		if !ok {
			continue
		}
		summary, ok := actor.StateSummary()
		if !ok {
			continue
		}
		out = append(out, observeProjectSession(summary))
	}
	return out, nil
}

func (o *observeSessionSource) ObservationSession(ctx context.Context, sessionID string) (runtimeobserve.SessionSummary, bool, error) {
	hub := o.hub()
	if hub == nil {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	actor, ok := hub.Get(sessionID)
	if !ok {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	summary, ok := actor.StateSummary()
	if !ok {
		return runtimeobserve.SessionSummary{}, false, nil
	}
	return observeProjectSession(summary), true, nil
}

// observeProjectSession 把 chat.RuntimeStateSummary 投影为低敏 SessionSummary。
// 只透传 status/turn id 等公开状态；trace/revision/last_event 等依赖权重的字段
// 在 v1 阶段以 0/空处理，避免暴露内部序列。
func observeProjectSession(s chat.RuntimeStateSummary) runtimeobserve.SessionSummary {
	return runtimeobserve.SessionSummary{
		SessionID: s.SessionID,
		State:     string(s.Status),
		TurnID:    s.CurrentTurnID,
	}
}

// GET /api/runtime/observe/v1/capabilities
func (h *Handler) ObserveCapabilities(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	svc := h.ensureObserveService()
	if svc == nil {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("%s: runtime observation disabled", runtimeobserve.ErrCodeDisabled))
		return
	}
	h.observeWriteJSON(w, r, svc.Capabilities(), nil)
}

// GET /api/runtime/observe/v1/snapshot?include_sessions=0|1
func (h *Handler) ObserveSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	svc := h.ensureObserveService()
	if svc == nil {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("%s: runtime observation disabled", runtimeobserve.ErrCodeDisabled))
		return
	}
	includeSessions := true
	if v := strings.TrimSpace(r.URL.Query().Get("include_sessions")); v != "" {
		includeSessions = v != "0" && v != "false"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := svc.BuildSnapshot(ctx, includeSessions)
	if err != nil {
		h.writeError(w, observeHTTPStatus(err), err)
		return
	}
	h.observeWriteJSON(w, r, snapshot, nil)
}

// GET /api/runtime/observe/v1/sessions/{session_id}
func (h *Handler) ObserveSession(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	svc := h.ensureObserveService()
	if svc == nil {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("%s: runtime observation disabled", runtimeobserve.ErrCodeDisabled))
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["session_id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("%s: session_id required", runtimeobserve.ErrCodeInvalidRequest))
		return
	}
	summary, err := svc.SessionFor(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, observeHTTPStatus(err), err)
		return
	}
	h.observeWriteJSON(w, r, summary, nil)
}

// GET /api/runtime/observe/v1/events
func (h *Handler) ObserveEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	svc := h.ensureObserveService()
	if svc == nil {
		h.writeError(w, http.StatusForbidden, fmt.Errorf("%s: runtime observation disabled", runtimeobserve.ErrCodeDisabled))
		return
	}
	q, err := parseObserveEventQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := svc.QueryEvents(q)
	if err != nil {
		h.writeError(w, observeHTTPStatus(err), err)
		return
	}
	h.observeWriteJSON(w, r, result, nil)
}

// observeWriteJSON 输出统一 envelope；Data 为已投影的低敏载荷。
func (h *Handler) observeWriteJSON(w http.ResponseWriter, r *http.Request, data interface{}, warnings []string) {
	env := runtimeobserve.Envelope{
		OK:            true,
		SchemaVersion: runtimeobserve.SchemaVersionResponse,
		RequestID:     strings.TrimSpace(r.Header.Get("X-Request-ID")),
		Data:          data,
		Warnings:      warnings,
	}
	if svc := h.ensureObserveService(); svc != nil {
		caps := svc.Capabilities()
		env.Redaction = &caps.Redaction
	}
	h.writeJSON(w, http.StatusOK, env)
}

// observeHTTPStatus 把服务错误码映射为 HTTP 状态码。
func observeHTTPStatus(err error) int {
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

// parseObserveEventQuery 从 query 参数构造低敏事件查询条件。
func parseObserveEventQuery(r *http.Request) (runtimeobserve.EventQuery, error) {
	var q runtimeobserve.EventQuery
	values := r.URL.Query()
	q.SessionID = chat.NormalizeSessionID(values.Get("session_id"))
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
