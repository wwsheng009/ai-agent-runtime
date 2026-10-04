package commands

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

const chatWebMCPMutationTimeout = 60 * time.Second

// chatWebMCPAdminServiceFn 是 MCP 管理服务的构造入口；测试可注入替身。
var chatWebMCPAdminServiceFn = chatWebMCPAdminService

// chatWebMCPToolListerFn 返回工具清单数据源（生产为进程内 MCPManagerInstance）；测试可注入替身。
var chatWebMCPToolListerFn = func() mcpToolsLister { return MCPManagerInstance }

// HandleChatWebAPIMCPs 处理 MCP 列表 / 新增：
//
//	GET  /web/api/mcps → {"count":N,"mcps":[{config,status}...]}
//	POST /web/api/mcps → 新增（写 mcp.yaml + 热重载生效）
//
// 写操作由 ChatWebAuthGuard 统一校验写令牌（X-AICLI-Token）。
func HandleChatWebAPIMCPs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		service, err := chatWebMCPAdminServiceFn()
		if err != nil {
			writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		items, err := service.List(ctx)
		if err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError, webMCPErrorBody("mcp_list_failed", err.Error()))
			return
		}
		payload := map[string]interface{}{
			"count": len(items),
			"mcps":  items,
		}
		if provider, ok := service.(mcpadmin.DiagnosticsProvider); ok {
			if diagnostics := provider.ConfigDiagnostics(); diagnostics != nil {
				payload["config"] = diagnostics
			}
		}
		payload["summary"] = mcpadmin.Summarize(items)
		// 会话级覆盖（/mcp --session 与 MCP 页签「本会话」按钮）随列表返回，
		// 客户端据此渲染「本会话已停用 / 临时启用」标记。
		if sessionScope := chatWebMCPSessionPayload(chatWebSession()); sessionScope != nil {
			payload["session"] = sessionScope
		}
		writeWebAPIJSON(w, http.StatusOK, payload)
	case http.MethodPost:
		service, err := chatWebMCPAdminServiceFn()
		if err != nil {
			writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
			return
		}
		request, err := decodeWebMCPUpsertRequest(r)
		if err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, webMCPErrorBody("invalid_request", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		mcpCfg, err := service.Add(ctx, request)
		if err != nil {
			writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_add_failed", err.Error()))
			return
		}
		writeWebAPIJSON(w, http.StatusCreated, map[string]interface{}{"config": mcpCfg})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only GET/POST are supported"))
	}
}

// HandleChatWebAPIMCP 处理单个 MCP 的子路径操作：
//
//	POST   /web/api/mcps/reload
//	GET    /web/api/mcps/{name}
//	PUT    /web/api/mcps/{name}
//	DELETE /web/api/mcps/{name}
//	POST   /web/api/mcps/{name}/enable | /disable
func HandleChatWebAPIMCP(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, ChatWebAPIMCPsPath), "/")
	if rest == "" {
		writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", "missing MCP name"))
		return
	}
	if rest == "reload" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only POST is supported"))
			return
		}
		service, err := chatWebMCPAdminServiceFn()
		if err != nil {
			writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		if err := service.Reload(ctx); err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError, webMCPErrorBody("mcp_reload_failed", err.Error()))
			return
		}
		items, _ := service.List(ctx)
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
			"reloaded": true,
			"count":    len(items),
		})
		return
	}

	segments := strings.Split(rest, "/")
	if len(segments) > 4 || len(segments) == 0 || strings.TrimSpace(segments[0]) == "" {
		writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", "invalid MCP path: "+rest))
		return
	}
	name := segments[0]

	if len(segments) == 2 && segments[1] == "tools" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only GET is supported"))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, chatWebMCPToolsBody(chatWebMCPToolListerFn(), name))
		return
	}

	if len(segments) >= 3 && segments[1] == "tools" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only POST is supported"))
			return
		}
		service, err := chatWebMCPAdminServiceFn()
		if err != nil {
			writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		switch {
		case len(segments) == 3 && (segments[2] == "enable" || segments[2] == "disable"):
			enabled := segments[2] == "enable"
			payload, decodeErr := decodeWebMCPToolsPayload(r)
			if decodeErr != nil {
				writeWebAPIJSON(w, http.StatusBadRequest, webMCPErrorBody("invalid_request", decodeErr.Error()))
				return
			}
			toggler, ok := service.(interface {
				SetToolsEnabled(context.Context, string, []string, bool) (*mcpconfig.MCPConfig, error)
			})
			if !ok {
				writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", "tool toggle not supported"))
				return
			}
			if _, err := toggler.SetToolsEnabled(ctx, name, payload.Tools, enabled); err != nil {
				writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_tools_update_failed", err.Error()))
				return
			}
			writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"name": name, "enabled": enabled, "tools": payload.Tools})
		case len(segments) == 4 && (segments[3] == "enable" || segments[3] == "disable"):
			tool := strings.TrimSpace(segments[2])
			if tool == "" {
				writeWebAPIJSON(w, http.StatusBadRequest, webMCPErrorBody("invalid_request", "tool name is required"))
				return
			}
			enabled := segments[3] == "enable"
			toggler, ok := service.(interface {
				SetToolEnabled(context.Context, string, string, bool) (*mcpconfig.MCPConfig, error)
			})
			if !ok {
				writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", "tool toggle not supported"))
				return
			}
			if _, err := toggler.SetToolEnabled(ctx, name, tool, enabled); err != nil {
				writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_tool_update_failed", err.Error()))
				return
			}
			writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"name": name, "tool": tool, "enabled": enabled})
		default:
			writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", "unknown tools action: "+rest))
		}
		return
	}

	if len(segments) > 2 {
		writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", "invalid MCP path: "+rest))
		return
	}

	if len(segments) == 2 {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only POST is supported"))
			return
		}
		var enabled bool
		switch segments[1] {
		case "enable":
			enabled = true
		case "disable":
			enabled = false
		default:
			writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", "unknown MCP action: "+segments[1]))
			return
		}
		// 会话级启停（?scope=session / body {"session":true}）：不写配置文件，
		// 只影响当前 chat/web 会话工具面（与 TUI `/mcp ... --session` 同一实现）。
		if webMCPRequestSessionScoped(r) {
			writeWebMCPSessionToggle(w, name, enabled)
			return
		}
		service, err := chatWebMCPAdminServiceFn()
		if err != nil {
			writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		mcpCfg, err := service.SetEnabled(ctx, name, enabled)
		if err != nil {
			writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_enable_failed", err.Error()))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"config": mcpCfg})
		return
	}

	service, err := chatWebMCPAdminServiceFn()
	if err != nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_unavailable", err.Error()))
		return
	}

	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		mcpCfg, err := service.Get(ctx, name)
		if err != nil {
			writeWebAPIJSON(w, http.StatusNotFound, webMCPErrorBody("mcp_not_found", err.Error()))
			return
		}
		body := map[string]interface{}{"config": mcpCfg}
		if state := chatWebMCPSessionState(chatWebSession(), name); state != "" {
			body["session_state"] = state
		}
		writeWebAPIJSON(w, http.StatusOK, body)
	case http.MethodPut:
		request, err := decodeWebMCPUpsertRequest(r)
		if err != nil {
			writeWebAPIJSON(w, http.StatusBadRequest, webMCPErrorBody("invalid_request", err.Error()))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		mcpCfg, err := service.Update(ctx, name, request)
		if err != nil {
			writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_update_failed", err.Error()))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"config": mcpCfg})
	case http.MethodDelete:
		ctx, cancel := context.WithTimeout(r.Context(), chatWebMCPMutationTimeout)
		defer cancel()
		if err := service.Remove(ctx, name); err != nil {
			writeWebAPIJSON(w, webMCPErrorStatus(err, http.StatusInternalServerError), webMCPErrorBody("mcp_remove_failed", err.Error()))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{"name": name, "removed": true})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, webMCPErrorBody("method_not_allowed", "only GET/PUT/DELETE are supported"))
	}
}

// chatWebMCPAdminService 构造当前 chat 进程的 MCP 管理服务。
//
// 复用进程内 MCPManagerInstance，保证新增/启停后的工具能立即注册回当前会话；
// 变更后的 refresh 回调把 MCP 工具重新登记到 FunctionCatalog（与 chat 启动一致）。
func chatWebMCPAdminService() (mcpadmin.AdminService, error) {
	cfg := agentconfig.GetGlobalConfig()
	configPath := strings.TrimSpace(resolveChatMCPConfigPath(cfg, chatWebSession()))
	if configPath == "" {
		configPath = resolveMCPConfigPathForWrite()
	}
	if err := mcpadmin.EnsureFile(configPath); err != nil {
		return nil, err
	}
	if err := initMCPManagerWithSelectionOverride(configPath, resolveChatMCPConfigOverride(cfg, chatWebSession()),
		runtimeprofileinput.ResolvedMCPSelection{}, false); err != nil {
		return nil, err
	}
	options := []mcpadmin.Option{
		mcpadmin.WithManager(MCPManagerInstance),
		mcpadmin.WithManagerFactory(func() manager.Manager { return manager.NewManager() }),
		mcpadmin.WithRefresh(refreshChatWebMCPTools),
	}
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		// 记录配置来源与候选清单，便于诊断“不同 CWD 解析到不同 mcp.yaml”。
		resolution := aiclipaths.ResolveMCPConfigPathDetailed(cfg.AICLI.MCP.ConfigFile)
		if strings.TrimSpace(resolution.Path) != "" {
			if !strings.EqualFold(filepath.Clean(resolution.Path), filepath.Clean(configPath)) {
				resolution.Path = configPath
				resolution.Source = "session-override"
			}
			options = append(options, mcpadmin.WithConfigDiagnostics(mcpadmin.ConfigDiagnosticsFromResolution(resolution)))
		}
	}
	return mcpadmin.NewService(configPath, options...), nil
}

// refreshChatWebMCPTools 把（重连后的）MCP 工具重新注册进当前会话的 FunctionCatalog，
// 并应用会话级覆盖（--session 停用的 server 不登记/被撤销；临时连接的迟到工具补登记）。
func refreshChatWebMCPTools() {
	session := chatWebSession()
	if session == nil || session.FunctionCatalog == nil {
		return
	}
	// 会话级覆盖先行：临时连接工具登记 + 被停用 server 的函数撤销。
	// 即使进程级 manager 缺失（MCP 全关）也要执行——临时连接可能仍活跃。
	refreshSessionScopedMCP(session)
	if MCPManagerInstance == nil {
		return
	}
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(
		MCPManagerInstance,
		loadRuntimeToolConfig(agentconfig.GetGlobalConfig(), session),
	)
	for _, desc := range toolManager.ListTools() {
		if sessionMCPOverrideDisabled(session, mcpNameFromDescriptor(desc)) {
			continue
		}
		session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(toolManager, desc), desc)
	}
	// 管理面变更（热重载/服务启停/工具启停）后让活跃会话在下个 turn 重建工具面。
	invalidateChatSessionToolSurfaces()
}

// chatWebMCPSessionState 返回单个 server 的会话级状态："disabled" / "temp" / ""。
func chatWebMCPSessionState(session *ChatSession, name string) string {
	if session == nil {
		return ""
	}
	if temp := session.mcpSessionTemp; temp != nil && temp.hasServer(name) {
		return "temp"
	}
	if sessionMCPOverrideDisabled(session, name) {
		return "disabled"
	}
	return ""
}

// chatWebMCPSessionPayload 渲染列表端点的会话级覆盖块；无覆盖时返回 nil（字段缺席）。
func chatWebMCPSessionPayload(session *ChatSession) map[string]interface{} {
	if session == nil {
		return nil
	}
	disabled := make([]string, 0, len(session.MCPSessionOverrides))
	for name, enabled := range session.MCPSessionOverrides {
		if !enabled {
			disabled = append(disabled, name)
		}
	}
	temp := make([]string, 0, 1)
	if session.mcpSessionTemp != nil {
		for name := range session.mcpSessionTemp.servers {
			temp = append(temp, name)
		}
	}
	if len(disabled) == 0 && len(temp) == 0 {
		return nil
	}
	sort.Strings(disabled)
	sort.Strings(temp)
	return map[string]interface{}{
		"disabled": disabled,
		"temp":     temp,
	}
}

// webMCPRequestSessionScoped 解析会话级开关：?scope=session / ?session=1，
// 或 JSON body {"session":true} / {"scope":"session"}。空 body 视为非会话级。
func webMCPRequestSessionScoped(r *http.Request) bool {
	if r == nil {
		return false
	}
	query := r.URL.Query()
	for _, key := range []string{"scope", "session"} {
		switch strings.ToLower(strings.TrimSpace(query.Get(key))) {
		case "session", "1", "true", "yes":
			return true
		}
	}
	if r.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	var payload struct {
		Session *bool  `json:"session"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	if payload.Session != nil {
		return *payload.Session
	}
	return strings.EqualFold(strings.TrimSpace(payload.Scope), "session")
}

// writeWebMCPSessionToggle 执行会话级启停（不写配置文件；全局停用时临时连接），
// 返回 scope/session_state 与人类可读回执，错误按 400 返回。
func writeWebMCPSessionToggle(w http.ResponseWriter, name string, enabled bool) {
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, webMCPErrorBody("mcp_session_unavailable", "no active chat session"))
		return
	}
	text, mutated := chatMCPSessionToggleText(session, name, enabled)
	if strings.HasPrefix(strings.TrimSpace(text), "错误:") {
		writeWebAPIJSON(w, http.StatusBadRequest, webMCPErrorBody("mcp_session_toggle_failed", text))
		return
	}
	if mutated {
		// web 会话的工具注册/失效通道（与 TUI refreshChatMCPTools 等价）。
		refreshChatWebMCPTools()
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
		"name":          name,
		"enabled":       enabled,
		"scope":         "session",
		"session_state": chatWebMCPSessionState(session, name),
		"message":       text,
	})
}

func decodeWebMCPUpsertRequest(r *http.Request) (mcpadmin.UpsertRequest, error) {
	var request mcpadmin.UpsertRequest
	if r == nil || r.Body == nil {
		return request, nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	return request, nil
}

// webMCPToolsPayload 批量工具启停请求体；tools 缺省（空数组）= 全部工具。
type webMCPToolsPayload struct {
	Tools []string `json:"tools"`
}

func decodeWebMCPToolsPayload(r *http.Request) (webMCPToolsPayload, error) {
	var payload webMCPToolsPayload
	if r == nil || r.Body == nil {
		return payload, nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil && !stderrors.Is(err, io.EOF) {
		return payload, err
	}
	return payload, nil
}

func webMCPErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}

// webMCPErrorStatus 按错误类型映射 HTTP 状态：校验失败 400 / 不存在 404 / 其它 fallback。
func webMCPErrorStatus(err error, fallback int) int {
	var validationErr *mcpadmin.ValidationError
	if stderrors.As(err, &validationErr) {
		return http.StatusBadRequest
	}
	var notFoundErr *mcpadmin.NotFoundError
	if stderrors.As(err, &notFoundErr) {
		return http.StatusNotFound
	}
	return fallback
}

// mcpToolsLister 抽出 ListTools 以便测试注入替身（生产实现是 manager.Manager）。
type mcpToolsLister interface {
	ListTools() []*mcpregistry.ToolInfo
}

// chatWebMCPToolsBody 汇总某个 MCP 当前暴露的工具清单（供微型 Web / console 的「查看工具」面板）。
// 未连接 / 未启用时返回空列表，由前端渲染空态；连接状态由 /web/api/mcps 提供。
func chatWebMCPToolsBody(lister mcpToolsLister, name string) map[string]interface{} {
	type toolEntry struct {
		Name              string                 `json:"name"`
		Description       string                 `json:"description,omitempty"`
		Enabled           bool                   `json:"enabled"`
		ConfiguredEnabled bool                   `json:"configured_enabled"`
		Healthy           bool                   `json:"healthy"`
		InputSchema       map[string]interface{} `json:"inputSchema,omitempty"`
	}

	tools := make([]toolEntry, 0)
	for _, info := range chatWebMCPToolInfos(lister, name) {
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
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	return map[string]interface{}{
		"name":  name,
		"count": len(tools),
		"tools": tools,
	}
}

// chatWebMCPToolInfos 返回某个 MCP 的全部工具；管理器支持全量清单时包含被禁用工具。
func chatWebMCPToolInfos(lister mcpToolsLister, name string) []*mcpregistry.ToolInfo {
	if full, ok := lister.(interface {
		ListAllToolsForMCP(string) []*mcpregistry.ToolInfo
	}); ok {
		return full.ListAllToolsForMCP(name)
	}
	tools := make([]*mcpregistry.ToolInfo, 0)
	if lister == nil {
		return tools
	}
	for _, info := range lister.ListTools() {
		if info == nil || info.Tool == nil || !strings.EqualFold(strings.TrimSpace(info.MCPName), strings.TrimSpace(name)) {
			continue
		}
		tools = append(tools, info)
	}
	return tools
}
