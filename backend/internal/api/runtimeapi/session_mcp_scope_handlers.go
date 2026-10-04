package runtimeapi

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	internalerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
)

// ListSessionRuntimeMCPs 返回某会话的 MCP 会话级覆盖 + 会话实际生效的配置面：
//
//	GET /api/runtime/sessions/{id}/runtime/mcps
//	→ {"session_id":"...","disabled":["name"],"count":1,
//	   "scope":{"read":{...},"write":{...},"workspace":"...","candidates":[...]},
//	   "mcps":[{config,status,source,session_disabled}],"summary":{...}}
//
// 空覆盖时 disabled 为空数组（不是 null），消费方可据此直接渲染"全部全局面"。
// mcps 是会话真实加载的条目（工作区锚定注入后来自工作区配置链；工作区还没有
// 配置文件时回退进程级），source 标注条目来自 workspace 还是 global。
func (h *Handler) ListSessionRuntimeMCPs(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "session id is required"))
		return
	}
	session := h.sessionByID(r.Context(), sessionID)
	if session == nil {
		h.writeError(w, http.StatusNotFound, internalerrors.New(internalerrors.ErrValidationFailed, "session not found"))
		return
	}
	disabled := sessionMCPDisabledNames(session)
	if disabled == nil {
		disabled = []string{}
	}
	disabledSet := make(map[string]struct{}, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = struct{}{}
	}

	scope := h.sessionMCPAdminScopeFor(session)
	payload := map[string]interface{}{
		"session_id": sessionID,
		"disabled":   disabled,
		"count":      len(disabled),
		"scope":      scope.payload(),
	}

	source := "global"
	if scope.WorkspaceScoped && !scope.WorkspaceFallback {
		source = "workspace"
	}
	service := h.sessionMCPAdminService(scope, false)
	if service == nil {
		payload["mcps"] = []sessionMCPItem{}
		payload["summary"] = mcpadmin.Summarize(nil)
		h.writeJSON(w, http.StatusOK, payload)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, internalerrors.New(internalerrors.ErrConfigInvalid,
			"failed to list session MCPs: "+err.Error()))
		return
	}
	entries := make([]sessionMCPItem, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Config.Name)
		_, sessionDisabled := disabledSet[name]
		entries = append(entries, sessionMCPItem{
			Item:            item,
			Source:          source,
			SessionDisabled: sessionDisabled,
		})
	}
	payload["mcps"] = entries
	payload["summary"] = mcpadmin.Summarize(items)
	h.writeJSON(w, http.StatusOK, payload)
}

// EnableSessionRuntimeMCP 会话级恢复：清除该 server 的会话停用覆盖。
//
//	POST /api/runtime/sessions/{id}/runtime/mcps/{name}/enable
func (h *Handler) EnableSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.handleSessionRuntimeMCPToggle(w, r, true)
}

// DisableSessionRuntimeMCP 会话级停用：只收窄本会话工具面，不写配置文件。
//
//	POST /api/runtime/sessions/{id}/runtime/mcps/{name}/disable
func (h *Handler) DisableSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.handleSessionRuntimeMCPToggle(w, r, false)
}

func (h *Handler) handleSessionRuntimeMCPToggle(w http.ResponseWriter, r *http.Request, enabled bool) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("scope")), "workspace") {
		h.handleSessionWorkspaceMCPToggle(w, r, enabled)
		return
	}
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if sessionID == "" || name == "" {
		h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "session id and mcp name are required"))
		return
	}
	result, err := h.applyRuntimeSessionMCPToggle(r.Context(), sessionID, name, enabled)
	if err != nil {
		switch {
		case stderrors.Is(err, errRuntimeSessionMCPTempUnsupported):
			h.writeError(w, http.StatusConflict, internalerrors.New(internalerrors.ErrValidationFailed, err.Error()))
		case stderrors.Is(err, errRuntimeSessionMCPNotFound):
			h.writeError(w, http.StatusNotFound, internalerrors.New(internalerrors.ErrValidationFailed, err.Error()))
		default:
			h.writeError(w, http.StatusInternalServerError, internalerrors.New(internalerrors.ErrValidationFailed, err.Error()))
		}
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":    sessionID,
		"name":          name,
		"enabled":       enabled,
		"scope":         "session",
		"session_state": result.State,
		"changed":       result.Changed,
		"message":       result.Message,
	})
}
