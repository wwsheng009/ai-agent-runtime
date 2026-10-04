package runtimeapi

// 会话级 MCP 变更端点（工作区锚定）：
//
//	POST   /api/runtime/sessions/{id}/runtime/mcps                    新增（写工作区/进程级配置文件）
//	PUT    /api/runtime/sessions/{id}/runtime/mcps/{name}             更新
//	DELETE /api/runtime/sessions/{id}/runtime/mcps/{name}             删除
//	POST   /api/runtime/sessions/{id}/runtime/mcps/{name}/enable|disable?scope=workspace
//	                                                                  持久化启停（写配置文件 + 热重载）
//	POST   /api/runtime/sessions/{id}/runtime/mcps/reload             重载会话配置面
//
// 无 scope=workspace 的 enable/disable 仍是「仅本会话覆盖」（session_mcp_scope.go 的
// 既有语义），两条路径互不影响。所有写操作后失效该会话工具面缓存，下个 turn 边界生效。

import (
	"context"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	internalerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
)

// sessionMCPItem 会话 MCP 列表条目：配置/状态 + 来源 + 会话覆盖标记。
type sessionMCPItem struct {
	mcpadmin.Item
	Source          string `json:"source"` // workspace | global
	SessionDisabled bool   `json:"session_disabled"`
}

// sessionMCPAdminTarget 读取会话并解析管理目标；调用方据返回值分派状态码。
func (h *Handler) sessionMCPAdminTarget(r *http.Request) (string, *chat.Session, *sessionMCPAdminScope) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		return "", nil, nil
	}
	session := h.sessionByID(r.Context(), sessionID)
	if session == nil {
		return sessionID, nil, nil
	}
	return sessionID, session, h.sessionMCPAdminScopeFor(session)
}

// sessionMCPAdminScopeName 返回面向响应的 scope 名（workspace/global）。
func sessionMCPAdminScopeName(scope *sessionMCPAdminScope) string {
	if scope != nil && scope.WorkspaceScoped {
		return "workspace"
	}
	return "global"
}

// requireSessionMCPMutation 统一处理鉴权与目标解析；失败时已写响应并返回 ok=false。
func (h *Handler) requireSessionMCPMutation(w http.ResponseWriter, r *http.Request, needName bool) (string, *sessionMCPAdminScope, string, bool) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return "", nil, "", false
	}
	sessionID, session, scope := h.sessionMCPAdminTarget(r)
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "session id is required"))
		return "", nil, "", false
	}
	if session == nil {
		h.writeError(w, http.StatusNotFound, internalerrors.New(internalerrors.ErrValidationFailed, "session not found"))
		return "", nil, "", false
	}
	name := ""
	if needName {
		name = strings.TrimSpace(mux.Vars(r)["name"])
		if name == "" {
			h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "MCP 名称不能为空"))
			return "", nil, "", false
		}
	}
	return sessionID, scope, name, true
}

// handleSessionWorkspaceMCPToggle 持久化启停：写会话生效的配置文件并热重载。
func (h *Handler) handleSessionWorkspaceMCPToggle(w http.ResponseWriter, r *http.Request, enabled bool) {
	sessionID, scope, name, ok := h.requireSessionMCPMutation(w, r, true)
	if !ok {
		return
	}
	service := h.sessionMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, internalerrors.New(internalerrors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置会话可写的 MCP 配置）"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.SetEnabled(ctx, name, enabled); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP enabled state")
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	eventType := "mcp.disabled"
	if enabled {
		eventType = "mcp.enabled"
	}
	h.afterRuntimeMCPMutation(r.Context(), eventType, name)

	message := "已停用 MCP " + name + "（写入会话配置）"
	if enabled {
		message = "已启用 MCP " + name + "（写入会话配置）"
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":  sessionID,
		"name":        name,
		"enabled":     enabled,
		"scope":       sessionMCPAdminScopeName(scope),
		"changed":     true,
		"config_path": scope.WritePath,
		"message":     message,
	})
}

// AddSessionRuntimeMCP 在会话生效的配置文件中新增 MCP 并热重载。
func (h *Handler) AddSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, _, ok := h.requireSessionMCPMutation(w, r, false)
	if !ok {
		return
	}
	service := h.sessionMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, internalerrors.New(internalerrors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置会话可写的 MCP 配置）"))
		return
	}
	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "invalid MCP payload: "+err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Add(ctx, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to add MCP")
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	h.afterRuntimeMCPMutation(r.Context(), "mcp.created", request.Name)
	h.writeMCPAdminResult(w, service, r, request.Name, http.StatusCreated)
}

// UpdateSessionRuntimeMCP 更新会话配置文件中的 MCP 并热重载。
func (h *Handler) UpdateSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, name, ok := h.requireSessionMCPMutation(w, r, true)
	if !ok {
		return
	}
	service := h.sessionMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, internalerrors.New(internalerrors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置会话可写的 MCP 配置）"))
		return
	}
	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, internalerrors.New(internalerrors.ErrValidationFailed, "invalid MCP payload: "+err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Update(ctx, name, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP")
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	h.afterRuntimeMCPMutation(r.Context(), "mcp.updated", name)
	h.writeMCPAdminResult(w, service, r, name, http.StatusOK)
}

// RemoveSessionRuntimeMCP 删除会话配置文件中的 MCP 并热重载。
func (h *Handler) RemoveSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, name, ok := h.requireSessionMCPMutation(w, r, true)
	if !ok {
		return
	}
	service := h.sessionMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, internalerrors.New(internalerrors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置会话可写的 MCP 配置）"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if err := service.Remove(ctx, name); err != nil {
		h.writeMCPAdminError(w, err, "failed to remove MCP")
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	h.afterRuntimeMCPMutation(r.Context(), "mcp.removed", name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"name":       name,
		"removed":    true,
		"scope":      sessionMCPAdminScopeName(scope),
	})
}

// ReloadSessionRuntimeMCP 重载会话生效的配置文件（工作区 manager 原地重连）。
func (h *Handler) ReloadSessionRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	sessionID, scope, _, ok := h.requireSessionMCPMutation(w, r, false)
	if !ok {
		return
	}
	service := h.sessionMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, internalerrors.New(internalerrors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置会话可写的 MCP 配置）"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if err := service.Reload(ctx); err != nil {
		h.writeMCPAdminError(w, err, "failed to reload MCP config")
		return
	}
	h.invalidateSessionMCPRuntimeSurface(sessionID)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"reloaded":   true,
		"scope":      sessionMCPAdminScopeName(scope),
		"path":       scope.WritePath,
	})
}
