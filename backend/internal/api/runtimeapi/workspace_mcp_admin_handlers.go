package runtimeapi

// 工作区级 MCP 管理端点：不依赖 session，供新会话首轮消息之前配置当前工作区。
//
// 与 session_workspace_mcp_admin.go 的区别：
//   - workspace path 来自请求并经过 workspaceregistry.NormalizePath；
//   - 只展示/修改 workspace 配置面，不把进程级 fallback 条目伪装成 workspace 条目；
//   - session-only 覆盖仍只能通过 /sessions/{id}/runtime/mcps 使用。

import (
	"context"
	stderrors "errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	internalerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspaceregistry"
)

const workspaceMCPPathQuery = "workspace_path"

// workspaceMCPAdminScopeForPath resolves the workspace read/write target.
// A global/user/executable winner is deliberately treated as fallback: the
// workspace API must never turn a GET/POST for one workspace into a global
// config mutation.
func (h *Handler) workspaceMCPAdminScopeForPath(rawPath string) (*sessionMCPAdminScope, error) {
	workspace, err := workspaceregistry.NormalizePath(rawPath)
	if err != nil {
		return nil, err
	}

	globalPath := strings.TrimSpace(h.profileGlobalMCPPath)
	explicit := ""
	if h.workspaceMCP != nil {
		if configured := strings.TrimSpace(h.workspaceMCP.cfg.GlobalPath); configured != "" {
			globalPath = configured
		}
		explicit = strings.TrimSpace(h.workspaceMCP.cfg.ExplicitOverride)
	}
	var resolution aiclipaths.MCPConfigResolution
	if h.workspaceMCP != nil {
		resolution = h.workspaceMCP.resolve(workspace)
	} else {
		resolution = aiclipaths.ResolveMCPConfigPathDetailedFrom(workspace, explicit)
	}

	scope := &sessionMCPAdminScope{
		Workspace:  workspace,
		Resolution: resolution,
	}
	projectTarget := filepath.Join(
		workspace,
		".aicli",
		aiclipaths.DefaultMCPConfigFileName,
	)
	winner := strings.TrimSpace(resolution.Path)
	workspaceWinner := winner != "" &&
		!sameAbsPath(winner, globalPath) &&
		isWorkspaceMCPSource(resolution.Source)

	if workspaceWinner {
		scope.ReadPath = winner
		scope.ReadSource = resolution.Source
		scope.ReadExists = fileExists(winner)
		scope.WritePath = scope.ReadPath
		scope.WriteSource = scope.ReadSource
		scope.WriteExists = scope.ReadExists
		scope.WorkspaceScoped = true
		return scope, nil
	}

	// No workspace file yet (or the resolver only found the global file): reads
	// are empty and writes go to the workspace project layer.
	scope.ReadPath = projectTarget
	scope.ReadSource = "project"
	scope.ReadExists = fileExists(projectTarget)
	scope.WritePath = projectTarget
	scope.WriteSource = "project"
	scope.WriteExists = scope.ReadExists
	scope.WorkspaceScoped = true
	scope.WorkspaceFallback = true
	return scope, nil
}

func isWorkspaceMCPSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "local", "project", "upward", "default":
		return true
	default:
		return false
	}
}

func (h *Handler) workspaceMCPAdminScopeFromRequest(r *http.Request) (*sessionMCPAdminScope, error) {
	if r == nil {
		return nil, stderrors.New("workspace path is required")
	}
	raw := strings.TrimSpace(r.URL.Query().Get(workspaceMCPPathQuery))
	if raw == "" {
		return nil, stderrors.New("workspace_path is required")
	}
	return h.workspaceMCPAdminScopeForPath(raw)
}

// workspaceRuntimeMCPAdminService returns a service restricted to the
// workspace scope. In fallback mode it is write-only until the first file is
// created; it must not load or mutate the global manager.
func (h *Handler) workspaceRuntimeMCPAdminService(
	scope *sessionMCPAdminScope,
	write bool,
) mcpadmin.AdminService {
	if h == nil || scope == nil || !scope.WorkspaceScoped {
		return nil
	}
	path := scope.ReadPath
	if write {
		path = scope.WritePath
	} else if !scope.ReadExists {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	options := make([]mcpadmin.Option, 0, 3)
	reused := false
	if !scope.WorkspaceFallback && h.workspaceMCP != nil {
		if entry := h.workspaceMCP.acquireEntry(scope.Workspace); entry != nil && entry.manager != nil {
			options = append(options, mcpadmin.WithManager(entry.manager))
			reused = true
		}
	}
	if !reused {
		// A workspace manager may not exist yet (notably on first write). Keep
		// persistence separate from runtime connection creation.
		options = append(options, mcpadmin.WithApplyOnMutate(false))
	}
	if diagnostics := scope.diagnostics(); diagnostics != nil {
		options = append(options, mcpadmin.WithConfigDiagnostics(*diagnostics))
	}
	return mcpadmin.NewService(path, options...)
}

func (h *Handler) requireWorkspaceMCPMutation(
	w http.ResponseWriter,
	r *http.Request,
) *sessionMCPAdminScope {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return nil
	}
	scope, err := h.workspaceMCPAdminScopeFromRequest(r)
	if err != nil {
		h.writeWorkspaceMCPPathError(w, err)
		return nil
	}
	return scope
}

func (h *Handler) writeWorkspaceMCPPathError(w http.ResponseWriter, err error) {
	switch {
	case stderrors.Is(err, workspaceregistry.ErrValidationFailed),
		stderrors.Is(err, workspaceregistry.ErrDirectoryNotFound),
		stderrors.Is(err, workspaceregistry.ErrNotDirectory):
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed, err.Error()))
	default:
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed, err.Error()))
	}
}

func (h *Handler) workspaceMCPResponseScope(scope *sessionMCPAdminScope) map[string]interface{} {
	payload := scope.payload()
	payload["scope"] = "workspace"
	payload["workspace_path"] = scope.Workspace
	payload["config_path"] = scope.WritePath
	return payload
}

// ListWorkspaceRuntimeMCPs returns only the workspace configuration surface.
func (h *Handler) ListWorkspaceRuntimeMCPs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	scope, err := h.workspaceMCPAdminScopeFromRequest(r)
	if err != nil {
		h.writeWorkspaceMCPPathError(w, err)
		return
	}

	payload := h.workspaceMCPResponseScope(scope)
	payload["mcps"] = []workspaceMCPItem{}
	payload["count"] = 0
	payload["summary"] = mcpadmin.Summarize(nil)
	service := h.workspaceRuntimeMCPAdminService(scope, false)
	if service == nil {
		h.writeJSON(w, http.StatusOK, payload)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError,
			internalerrors.New(internalerrors.ErrConfigInvalid,
				"failed to list workspace MCPs: "+err.Error()))
		return
	}
	entries := make([]workspaceMCPItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, workspaceMCPItem{
			Item:   item,
			Source: "workspace",
		})
	}
	payload["mcps"] = entries
	payload["count"] = len(entries)
	payload["summary"] = mcpadmin.Summarize(items)
	h.writeJSON(w, http.StatusOK, payload)
}

type workspaceMCPItem struct {
	mcpadmin.Item
	Source string `json:"source"`
}

func (h *Handler) writeWorkspaceMCPMutationResult(
	w http.ResponseWriter,
	r *http.Request,
	scope *sessionMCPAdminScope,
	service mcpadmin.AdminService,
	name string,
	statusCode int,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	config, err := service.Get(ctx, name)
	if err != nil {
		h.writeMCPAdminError(w, err, "failed to load workspace MCP")
		return
	}
	h.writeJSON(w, statusCode, map[string]interface{}{
		"config":         config,
		"status":         nil,
		"scope":          "workspace",
		"workspace_path": scope.Workspace,
		"config_path":    scope.WritePath,
	})
}

func (h *Handler) AddWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	scope := h.requireWorkspaceMCPMutation(w, r)
	if scope == nil {
		return
	}
	service := h.workspaceRuntimeMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			internalerrors.New(internalerrors.ErrConfigInvalid,
				"workspace MCP management is unavailable"))
		return
	}
	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed,
				"invalid MCP payload: "+err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Add(ctx, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to add workspace MCP")
		return
	}
	h.afterRuntimeMCPMutation(r.Context(), "mcp.created", request.Name)
	h.writeWorkspaceMCPMutationResult(w, r, scope, service, request.Name, http.StatusCreated)
	h.invalidateWorkspaceMCPRuntimeSurface(r.Context(), scope)
}

func (h *Handler) UpdateWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	scope := h.requireWorkspaceMCPMutation(w, r)
	if scope == nil {
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	service := h.workspaceRuntimeMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			internalerrors.New(internalerrors.ErrConfigInvalid,
				"workspace MCP management is unavailable"))
		return
	}
	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed,
				"invalid MCP payload: "+err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Update(ctx, name, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to update workspace MCP")
		return
	}
	h.afterRuntimeMCPMutation(r.Context(), "mcp.updated", name)
	h.writeWorkspaceMCPMutationResult(w, r, scope, service, name, http.StatusOK)
	h.invalidateWorkspaceMCPRuntimeSurface(r.Context(), scope)
}

func (h *Handler) RemoveWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	scope := h.requireWorkspaceMCPMutation(w, r)
	if scope == nil {
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	service := h.workspaceRuntimeMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			internalerrors.New(internalerrors.ErrConfigInvalid,
				"workspace MCP management is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if err := service.Remove(ctx, name); err != nil {
		h.writeMCPAdminError(w, err, "failed to remove workspace MCP")
		return
	}
	h.invalidateWorkspaceMCPRuntimeSurface(r.Context(), scope)
	h.afterRuntimeMCPMutation(r.Context(), "mcp.removed", name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":           name,
		"removed":        true,
		"scope":          "workspace",
		"workspace_path": scope.Workspace,
		"config_path":    scope.WritePath,
	})
}

func (h *Handler) SetWorkspaceRuntimeMCPEnabled(
	w http.ResponseWriter,
	r *http.Request,
	enabled bool,
) {
	scope := h.requireWorkspaceMCPMutation(w, r)
	if scope == nil {
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest,
			internalerrors.New(internalerrors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	service := h.workspaceRuntimeMCPAdminService(scope, true)
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable,
			internalerrors.New(internalerrors.ErrConfigInvalid,
				"workspace MCP management is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.SetEnabled(ctx, name, enabled); err != nil {
		h.writeMCPAdminError(w, err, "failed to update workspace MCP enabled state")
		return
	}
	h.invalidateWorkspaceMCPRuntimeSurface(r.Context(), scope)
	eventType := "mcp.disabled"
	if enabled {
		eventType = "mcp.enabled"
	}
	h.afterRuntimeMCPMutation(r.Context(), eventType, name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":           name,
		"enabled":        enabled,
		"scope":          "workspace",
		"workspace_path": scope.Workspace,
		"config_path":    scope.WritePath,
		"changed":        true,
	})
}

func (h *Handler) EnableWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.SetWorkspaceRuntimeMCPEnabled(w, r, true)
}

func (h *Handler) DisableWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.SetWorkspaceRuntimeMCPEnabled(w, r, false)
}

func (h *Handler) ReloadWorkspaceRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	scope := h.requireWorkspaceMCPMutation(w, r)
	if scope == nil {
		return
	}
	// Reload is not a create operation: when the workspace currently falls
	// back to global (or its resolved workspace file was removed), do not let
	// the admin service's EnsureFile create an empty workspace config merely
	// because the client clicked reload.
	service := h.workspaceRuntimeMCPAdminService(scope, false)
	if service == nil {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"reloaded":       false,
			"scope":          "workspace",
			"workspace_path": scope.Workspace,
			"config_path":    scope.WritePath,
			"message":        "workspace MCP config is not loaded yet",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if err := service.Reload(ctx); err != nil {
		h.writeMCPAdminError(w, err, "failed to reload workspace MCP config")
		return
	}
	h.invalidateWorkspaceMCPRuntimeSurface(r.Context(), scope)
	h.afterRuntimeMCPMutation(r.Context(), "mcp.reloaded", "")
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reloaded":       true,
		"scope":          "workspace",
		"workspace_path": scope.Workspace,
		"config_path":    scope.WritePath,
	})
}
