package skills

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpcatalog "github.com/wwsheng009/ai-agent-runtime/internal/mcp/catalog"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

const runtimeMCPMutationTimeout = 60 * time.Second

// ListRuntimeMCPs 列出全部 MCP（配置 + 连接状态 + 工具数）。
func (h *Handler) ListRuntimeMCPs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to list MCPs", err))
		return
	}
	payload := map[string]interface{}{
		"mcps":  items,
		"count": len(items),
	}
	// 可选增强：暴露“实际读取的配置文件 / 解析来源 / 候选清单”，便于定位
	// “不同 CWD 启动解析到不同 mcp.yaml，导致某些 server 没连”一类问题。
	if provider, ok := service.(mcpadmin.DiagnosticsProvider); ok {
		if diagnostics := provider.ConfigDiagnostics(); diagnostics != nil {
			payload["config"] = diagnostics
		}
	}
	payload["summary"] = mcpadmin.Summarize(items)
	h.writeJSON(w, http.StatusOK, payload)
}

// ListRuntimeMCPTools 返回单个 MCP 当前暴露的工具清单（供 console / 微型 Web 的「查看工具」面板）。
//
//	GET /api/runtime/mcps/{name}/tools → {"name":..,"count":N,"tools":[{name,description,enabled,inputSchema}]}
func (h *Handler) ListRuntimeMCPTools(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	manager := h.runtimeMCPManager()
	if manager == nil && h.mcpManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP runtime manager not available"))
		return
	}

	type toolEntry struct {
		Name              string                 `json:"name"`
		Description       string                 `json:"description,omitempty"`
		Enabled           bool                   `json:"enabled"`
		ConfiguredEnabled bool                   `json:"configured_enabled"`
		Healthy           bool                   `json:"healthy"`
		InputSchema       map[string]interface{} `json:"inputSchema,omitempty"`
	}

	tools := make([]toolEntry, 0)
	if manager != nil {
		for _, info := range listRuntimeMCPTools(manager, name) {
			if info == nil || info.Tool == nil {
				continue
			}
			healthy := info.Enabled
			configured := !info.UserDisabled
			tools = append(tools, toolEntry{
				Name:              strings.TrimSpace(info.Tool.Name),
				Description:       strings.TrimSpace(info.Tool.Description),
				Enabled:           healthy && configured,
				ConfiguredEnabled: configured,
				Healthy:           healthy,
				InputSchema:       info.Tool.InputSchema,
			})
		}
	} else {
		// 回退：接口形态的 manager（MCPAdapter 等）暴露的是可调用工具名。
		for _, info := range h.mcpManager.ListTools() {
			if !strings.EqualFold(strings.TrimSpace(info.MCPName), name) {
				continue
			}
			tools = append(tools, toolEntry{
				Name:              strings.TrimSpace(info.Name),
				Description:       strings.TrimSpace(info.Description),
				Enabled:           info.Enabled,
				ConfiguredEnabled: info.Enabled,
				Healthy:           info.Enabled,
				InputSchema:       info.InputSchema,
			})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":  name,
		"count": len(tools),
		"tools": tools,
	})
}

// CreateRuntimeMCP 新增 MCP 并热重载生效。
func (h *Handler) CreateRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}

	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid MCP payload", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Add(ctx, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to add MCP")
		return
	}
	h.afterRuntimeMCPMutation(r.Context(), "mcp.created", request.Name)
	h.writeMCPAdminResult(w, service, r, request.Name, http.StatusCreated)
}

// UpdateRuntimeMCP 更新 MCP 配置并热重载生效。
func (h *Handler) UpdateRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	request, err := decodeRuntimeMCPRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid MCP payload", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.Update(ctx, name, request); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP")
		return
	}
	h.afterRuntimeMCPMutation(r.Context(), "mcp.updated", name)
	h.writeMCPAdminResult(w, service, r, name, http.StatusOK)
}

// DeleteRuntimeMCP 删除 MCP 并热重载生效。
func (h *Handler) DeleteRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if err := service.Remove(ctx, name); err != nil {
		h.writeMCPAdminError(w, err, "failed to remove MCP")
		return
	}
	h.afterRuntimeMCPMutation(r.Context(), "mcp.removed", name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"name": name, "removed": true})
}

// SetRuntimeMCPEnabled 启用/停用 MCP（持久化 + 热重载生效）。
func (h *Handler) SetRuntimeMCPEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	if _, err := service.SetEnabled(ctx, name, enabled); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP enabled state")
		return
	}
	eventType := "mcp.disabled"
	if enabled {
		eventType = "mcp.enabled"
	}
	h.afterRuntimeMCPMutation(r.Context(), eventType, name)
	h.writeMCPAdminResult(w, service, r, name, http.StatusOK)
}

// EnableRuntimeMCP 启用 MCP。
func (h *Handler) EnableRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPEnabled(w, r, true)
}

// DisableRuntimeMCP 停用 MCP。
func (h *Handler) DisableRuntimeMCP(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPEnabled(w, r, false)
}

// SetRuntimeMCPToolEnabled 启停单个 MCP 工具（持久化 + 运行时生效，不重连服务）。
func (h *Handler) SetRuntimeMCPToolEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	tool := strings.TrimSpace(mux.Vars(r)["tool"])
	if name == "" || tool == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称与工具名不能为空"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	toggler, ok := service.(interface {
		SetToolEnabled(context.Context, string, string, bool) (*mcpconfig.MCPConfig, error)
	})
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不支持工具级启停"))
		return
	}
	if _, err := toggler.SetToolEnabled(ctx, name, tool, enabled); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP tool enabled state")
		return
	}
	eventType := "mcp.tool.disabled"
	if enabled {
		eventType = "mcp.tool.enabled"
	}
	h.afterRuntimeMCPMutation(r.Context(), eventType, name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":    name,
		"tool":    tool,
		"enabled": enabled,
	})
}

// EnableRuntimeMCPTool 启用单个工具。
func (h *Handler) EnableRuntimeMCPTool(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPToolEnabled(w, r, true)
}

// DisableRuntimeMCPTool 停用单个工具。
func (h *Handler) DisableRuntimeMCPTool(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPToolEnabled(w, r, false)
}

// SetRuntimeMCPToolsEnabled 批量启停工具；body {"tools":[...]}，缺省/空数组 = 全部。
func (h *Handler) SetRuntimeMCPToolsEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := h.authorizeMCPAdminMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	service := h.runtimeMCPAdminService()
	if service == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不可用（未配置 MCP）"))
		return
	}
	name := strings.TrimSpace(mux.Vars(r)["name"])
	if name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "MCP 名称不能为空"))
		return
	}
	var payload struct {
		Tools []string `json:"tools"`
	}
	if r != nil && r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
		if err := decoder.Decode(&payload); err != nil && !stderrors.Is(err, io.EOF) {
			h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "invalid MCP tools payload", err))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), runtimeMCPMutationTimeout)
	defer cancel()
	toggler, ok := service.(interface {
		SetToolsEnabled(context.Context, string, []string, bool) (*mcpconfig.MCPConfig, error)
	})
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP 管理服务不支持工具级启停"))
		return
	}
	if _, err := toggler.SetToolsEnabled(ctx, name, payload.Tools, enabled); err != nil {
		h.writeMCPAdminError(w, err, "failed to update MCP tools enabled state")
		return
	}
	eventType := "mcp.tools.disabled"
	if enabled {
		eventType = "mcp.tools.enabled"
	}
	h.afterRuntimeMCPMutation(r.Context(), eventType, name)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":    name,
		"enabled": enabled,
		"tools":   payload.Tools,
	})
}

// EnableRuntimeMCPTools 批量启用工具。
func (h *Handler) EnableRuntimeMCPTools(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPToolsEnabled(w, r, true)
}

// DisableRuntimeMCPTools 批量停用工具。
func (h *Handler) DisableRuntimeMCPTools(w http.ResponseWriter, r *http.Request) {
	h.SetRuntimeMCPToolsEnabled(w, r, false)
}

// listRuntimeMCPTools 返回某个 MCP 的全部工具；runtime 管理器支持全量清单时包含被禁用工具。
func listRuntimeMCPTools(mgr mcpmanager.Manager, name string) []*mcpregistry.ToolInfo {
	if lister, ok := mgr.(interface {
		ListAllToolsForMCP(string) []*mcpregistry.ToolInfo
	}); ok {
		return lister.ListAllToolsForMCP(name)
	}
	tools := make([]*mcpregistry.ToolInfo, 0)
	for _, info := range mgr.ListTools() {
		if info == nil || info.Tool == nil || !strings.EqualFold(strings.TrimSpace(info.MCPName), name) {
			continue
		}
		tools = append(tools, info)
	}
	return tools
}

// authorizeMCPAdminMutation 校验 MCP 管理写操作权限与治理策略。
func (h *Handler) authorizeMCPAdminMutation(r *http.Request) error {
	if err := h.authorizeUsageAdmin(r); err != nil {
		return err
	}
	policy := h.getMutationPolicy()
	if policy.ReadOnly {
		return errors.New(errors.ErrAgentPermission, "MCP 管理写操作已被 read-only 策略禁用")
	}
	return nil
}

func (h *Handler) runtimeMCPAdminService() mcpadmin.AdminService {
	if h == nil || h.mcpAdmin == nil {
		return nil
	}
	return h.mcpAdmin
}

// writeMCPAdminError 按错误类型映射 HTTP 状态：校验失败 400 / 不存在 404 / 其它 500。
func (h *Handler) writeMCPAdminError(w http.ResponseWriter, err error, action string) {
	var validationErr *mcpadmin.ValidationError
	if stderrors.As(err, &validationErr) {
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, action, err))
		return
	}
	var notFoundErr *mcpadmin.NotFoundError
	if stderrors.As(err, &notFoundErr) {
		h.writeError(w, http.StatusNotFound, errors.Wrap(errors.ErrConfigInvalid, action, err))
		return
	}
	h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, action, err))
}

// afterRuntimeMCPMutation 刷新工具目录并发布生命周期事件。
func (h *Handler) afterRuntimeMCPMutation(ctx context.Context, eventType, name string) {
	if h == nil {
		return
	}
	stats := mcpcatalog.RefreshStats{}
	if gateway := h.getRuntimeToolCatalogGateway(); gateway != nil {
		stats = gateway.Refresh()
	}
	payload := map[string]interface{}{
		"mcp_name":     name,
		"tool_count":   stats.Added + stats.Updated + stats.Removed,
		"catalog_sync": stats.LastRefreshAt,
	}
	h.publishRuntimeEvent(eventType, "", payload)
	_ = ctx
}

func decodeRuntimeMCPRequest(r *http.Request) (mcpadmin.UpsertRequest, error) {
	var request mcpadmin.UpsertRequest
	if r == nil || r.Body == nil {
		return request, nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	return request, nil
}

// writeMCPAdminResult 返回操作后的单个 MCP 视图（配置 + 最新状态）。
func (h *Handler) writeMCPAdminResult(w http.ResponseWriter, service mcpadmin.AdminService, r *http.Request, name string, statusCode int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()

	var status *mcpconfig.MCPStatus
	if items, err := service.List(ctx); err == nil {
		for _, item := range items {
			if strings.EqualFold(item.Config.Name, name) {
				status = item.Status
				break
			}
		}
	}
	config, err := service.Get(ctx, name)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to load MCP", err))
		return
	}
	h.writeJSON(w, statusCode, map[string]interface{}{
		"config": config,
		"status": status,
	})
}
