package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// 会话权限模式 API（composer 权限选择器使用）。
//
// GET  /api/runtime/sessions/{id}/permission-mode  当前模式 + 后端支持的模式清单
// POST /api/runtime/sessions/{id}/permission-mode  {"mode":"accept_edits"}，运行中亦可切换
//
// 说明：模式清单由后端 runtimepolicy.SupportedModes() 提供，前端只做渲染，
// 避免前端写死枚举与后端策略漂移。plan 模式具有独立持久生命周期
// （进入需要 plan 文件路径与 previous_mode 记录），因此这里只允许「切出」，
// 切向 plan 时返回 400 并引导调用 /sessions/{id}/plan。

const (
	sessionPermissionModePlanHint = "plan 模式请使用 /api/runtime/sessions/{id}/plan 接口进入（需 plan_path）"
)

type sessionPermissionModeOption struct {
	Value             string `json:"value"`
	Label             string `json:"label"`
	Description       string `json:"description,omitempty"`
	Dangerous         bool   `json:"dangerous,omitempty"`
	RequiresPlanEntry bool   `json:"requires_plan_entry,omitempty"`
}

type sessionPermissionModeResponse struct {
	SessionID    string                       `json:"session_id"`
	Mode         string                       `json:"mode"`
	Requested    string                       `json:"requested_mode,omitempty"`
	PreviousMode string                       `json:"previous_mode,omitempty"`
	PlanActive   bool                         `json:"plan_active,omitempty"`
	PlanStatus   string                       `json:"plan_status,omitempty"`
	Updated      bool                         `json:"updated,omitempty"`
	Supported    []sessionPermissionModeOption `json:"supported_modes"`
}

type sessionPermissionModeRequest struct {
	Mode string `json:"mode"`
}

// GetSessionPermissionMode 返回当前会话权限模式与后端支持的模式清单。
func (h *Handler) GetSessionPermissionMode(w http.ResponseWriter, r *http.Request) {
	session, err := h.loadSessionForPlanMode(r)
	if err != nil {
		h.writePlanModeError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, buildSessionPermissionModeResponse(session, false))
}

// UpdateSessionPermissionMode 切换会话权限模式；会话运行中同样生效。
//
// Body: {"mode":"default|accept_edits|bypass_permissions"}（canonical 下划线式枚举）
// 切离 plan 模式时会关闭持久 plan 状态（decision=quit）。
func (h *Handler) UpdateSessionPermissionMode(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session manager not configured"))
		return
	}

	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	var req sessionPermissionModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}

	// 严格校验：未知模式直接拒绝，绝不静默降级（否则等于放宽策略）。
	mode, ok := runtimepolicy.ParseMode(req.Mode)
	if !ok {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"unsupported permission mode "+strings.TrimSpace(req.Mode)+" (default|accept_edits|bypass_permissions)"))
		return
	}
	if mode == runtimepolicy.ModePlan {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, sessionPermissionModePlanHint))
		return
	}

	// 优先走运行中的 actor：同步权限引擎与 RunMeta，执行中切换立即生效。
	if actor, found := h.sessionPlanModeActor(sessionID); found && actor != nil {
		if err := actor.SetPermissionMode(r.Context(), sessionID, mode); err != nil {
			h.writePlanModeError(w, err)
			return
		}
		ctx, cancel := sessionStoreQueryContext(r)
		defer cancel()
		session, err := h.sessionManager.GetSession(ctx, sessionID)
		if err != nil {
			writeSessionStoreError(w, err)
			return
		}
		h.writeJSON(w, http.StatusOK, buildSessionPermissionModeResponse(session, true))
		return
	}

	ctx, cancel := sessionStoreQueryContext(r)
	defer cancel()
	session, err := h.sessionManager.GetSession(ctx, sessionID)
	if err != nil {
		writeSessionStoreError(w, err)
		return
	}
	if _, err := chat.SetSessionPermissionMode(session, mode); err != nil {
		h.writePlanModeError(w, err)
		return
	}
	if err := h.sessionManager.Update(ctx, session); err != nil {
		writeSessionStoreError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, buildSessionPermissionModeResponse(session, true))
}

func buildSessionPermissionModeResponse(session *chat.Session, updated bool) sessionPermissionModeResponse {
	state := planmode.Load(session)
	planActive := planmode.IsActive(state)

	mode := sessionPermissionMode(session)
	if planActive {
		mode = string(runtimepolicy.ModePlan)
	}

	response := sessionPermissionModeResponse{
		SessionID:    sessionIDOf(session),
		Mode:         normalizePermissionModeText(mode),
		PreviousMode: strings.TrimSpace(state.PreviousMode),
		PlanActive:   planActive,
		PlanStatus:   strings.TrimSpace(string(state.Status)),
		Updated:      updated,
		Supported:    supportedSessionPermissionModes(),
	}
	if text := sessionPermissionModeRequested(session); text != "" {
		response.Requested = text
	}
	return response
}

func sessionIDOf(session *chat.Session) string {
	if session == nil {
		return ""
	}
	return session.ID
}

func sessionPermissionModeRequested(session *chat.Session) string {
	if session == nil {
		return ""
	}
	if text := sessionmeta.String(session.Metadata.Context, sessionmeta.RequestedPermissionMode); text != "" {
		return normalizePermissionModeText(text)
	}
	if raw, ok := session.GetContext(sessionmeta.RequestedPermissionMode); ok {
		if text := strings.TrimSpace(strings.ToLower(fmt.Sprint(raw))); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func normalizePermissionModeText(raw string) string {
	text := strings.TrimSpace(strings.ToLower(raw))
	if text == "" || text == "<nil>" {
		return string(runtimepolicy.ModeDefault)
	}
	return text
}

// supportedSessionPermissionModes 与后端策略枚举保持同源，避免前后端漂移。
func supportedSessionPermissionModes() []sessionPermissionModeOption {
	options := []sessionPermissionModeOption{
		{
			Value:       string(runtimepolicy.ModeDefault),
			Label:       "默认权限",
			Description: "每次写入/执行前询问确认，最安全",
		},
		{
			Value:       string(runtimepolicy.ModeAcceptEdits),
			Label:       "自动接受编辑",
			Description: "文件编辑自动通过，危险命令仍需确认",
		},
		{
			Value:             string(runtimepolicy.ModePlan),
			Label:             "计划模式",
			Description:       "只读探索 + 仅允许写入 plan 文件",
			RequiresPlanEntry: true,
		},
		{
			Value:       string(runtimepolicy.ModeBypassPermissions),
			Label:       "跳过权限校验",
			Description: "全部工具调用直接放行，仅在可信工作区使用",
			Dangerous:   true,
		},
	}

	supported := runtimepolicy.SupportedModes()
	if len(supported) == 0 {
		return options
	}
	byValue := make(map[string]sessionPermissionModeOption, len(options))
	for _, option := range options {
		byValue[option.Value] = option
	}
	ordered := make([]sessionPermissionModeOption, 0, len(supported))
	for _, mode := range supported {
		value := strings.ToLower(strings.TrimSpace(string(mode)))
		if option, ok := byValue[value]; ok {
			ordered = append(ordered, option)
			continue
		}
		ordered = append(ordered, sessionPermissionModeOption{Value: value, Label: value})
	}
	return ordered
}
