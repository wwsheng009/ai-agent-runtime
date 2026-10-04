package runtimeapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// sessionScopeFakeMCPManager 提供 ListTools/FindTool/CallTool 全链路，
// 用于验证会话级包装器的过滤与放行。
type sessionScopeFakeMCPManager struct {
	tools []skill.ToolInfo
	calls []string
}

func (f *sessionScopeFakeMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	for _, tool := range f.tools {
		if tool.Name == toolName {
			return tool, nil
		}
	}
	return skill.ToolInfo{}, fmt.Errorf("tool %q not found", toolName)
}

func (f *sessionScopeFakeMCPManager) CallTool(_ interface{}, mcpName, toolName string, _ map[string]interface{}) (interface{}, error) {
	f.calls = append(f.calls, mcpName+"/"+toolName)
	return "ok", nil
}

func (f *sessionScopeFakeMCPManager) ListTools() []skill.ToolInfo { return f.tools }

// sessionScopeMCPAdminStub 提供精确的 Get（未知 server 返回 NotFoundError）。
type sessionScopeMCPAdminStub struct {
	servers map[string]*mcpconfig.MCPConfig
}

func (s *sessionScopeMCPAdminStub) List(context.Context) ([]mcpadmin.Item, error) {
	return nil, nil
}

func (s *sessionScopeMCPAdminStub) Get(_ context.Context, name string) (*mcpconfig.MCPConfig, error) {
	if cfg, ok := s.servers[name]; ok {
		return cfg, nil
	}
	return nil, &mcpadmin.NotFoundError{Message: "missing"}
}

func (s *sessionScopeMCPAdminStub) Add(context.Context, mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *sessionScopeMCPAdminStub) Update(context.Context, string, mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *sessionScopeMCPAdminStub) Remove(context.Context, string) error {
	return fmt.Errorf("not implemented")
}

func (s *sessionScopeMCPAdminStub) SetEnabled(context.Context, string, bool) (*mcpconfig.MCPConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *sessionScopeMCPAdminStub) Reload(context.Context) error {
	return fmt.Errorf("not implemented")
}

func (s *sessionScopeMCPAdminStub) ConfigDiagnostics() *mcpadmin.ConfigDiagnostics { return nil }

func newSessionScopeTestHandler(t *testing.T) (*Handler, *chat.SessionManager, *chat.Session) {
	t.Helper()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	session, err := sessionManager.Create(context.Background(), "user-session-mcp-scope")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetSessionManager(sessionManager)
	return handler, sessionManager, session
}

// 包装器必须动态按会话覆盖过滤三接口：ListTools 隐藏、FindTool/CallTool 拒绝。
func TestSessionScopedMCPManagerFiltersDisabledServers(t *testing.T) {
	handler, sessionManager, session := newSessionScopeTestHandler(t)
	base := &sessionScopeFakeMCPManager{tools: []skill.ToolInfo{
		{Name: "chrome_tool", MCPName: "chrome-mcp", Enabled: true},
		{Name: "other_tool", MCPName: "other-mcp", Enabled: true},
	}}
	handler.mcpManager = base

	setSessionMCPDisabledNames(session, []string{"chrome-mcp"})
	if err := sessionManager.Update(context.Background(), session); err != nil {
		t.Fatalf("update session: %v", err)
	}

	wrapped := handler.sessionScopedMCPSurface(context.Background(), session.ID, base)
	infos := wrapped.ListTools()
	if len(infos) != 1 || infos[0].Name != "other_tool" {
		t.Fatalf("ListTools = %#v, want only other_tool", infos)
	}
	if _, err := wrapped.FindTool("chrome_tool"); err == nil {
		t.Fatal("FindTool must reject tools of a session-disabled MCP")
	}
	if _, err := wrapped.FindTool("other_tool"); err != nil {
		t.Fatalf("FindTool other_tool: %v", err)
	}
	if _, err := wrapped.CallTool(context.Background(), "chrome-mcp", "chrome_tool", nil); err == nil {
		t.Fatal("CallTool must reject a session-disabled MCP")
	}
	if _, err := wrapped.CallTool(context.Background(), "other-mcp", "other_tool", nil); err != nil {
		t.Fatalf("CallTool other-mcp: %v", err)
	}

	// 动态读取：清空覆盖后同一包装器立即恢复（不依赖重建）。
	stored, err := sessionManager.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	setSessionMCPDisabledNames(stored, nil)
	if err := sessionManager.Update(context.Background(), stored); err != nil {
		t.Fatalf("update session: %v", err)
	}
	if infos := wrapped.ListTools(); len(infos) != 2 {
		t.Fatalf("ListTools after restore = %#v, want 2", infos)
	}
	if _, err := wrapped.FindTool("chrome_tool"); err != nil {
		t.Fatalf("FindTool after restore: %v", err)
	}
}

// 会话级启停端点：不写配置文件、覆盖随会话元数据持久化、状态与错误码正确。
func TestSessionRuntimeMCPToggleAPI(t *testing.T) {
	handler, sessionManager, session := newSessionScopeTestHandler(t)
	handler.SetMCPAdminService(&sessionScopeMCPAdminStub{servers: map[string]*mcpconfig.MCPConfig{
		"chrome-mcp": {Name: "chrome-mcp", Enabled: true},
		"off-mcp":    {Name: "off-mcp", Enabled: false},
	}})
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	base := "/api/runtime/sessions/" + session.ID + "/runtime/mcps"

	// 停用：200 + session_state=disabled。
	rec := doMCPAdminRequest(router, http.MethodPost, base+"/chrome-mcp/disable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"scope":"session"`) || !strings.Contains(body, `"session_state":"disabled"`) {
		t.Fatalf("disable body = %s", body)
	}
	stored, _ := sessionManager.Get(context.Background(), session.ID)
	if names := sessionMCPDisabledNames(stored); len(names) != 1 || names[0] != "chrome-mcp" {
		t.Fatalf("persisted disabled = %#v", names)
	}

	// 列表端点暴露覆盖。
	rec = doMCPAdminRequest(router, http.MethodGet, base, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"chrome-mcp"`) {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 工具面响应带 mcp_session 覆盖块。
	toolsRec := doMCPAdminRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/tools", "")
	if toolsRec.Code != http.StatusOK || !strings.Contains(toolsRec.Body.String(), `"mcp_session"`) {
		t.Fatalf("tools status = %d, body = %s", toolsRec.Code, toolsRec.Body.String())
	}

	// 恢复：200 且覆盖被清除。
	rec = doMCPAdminRequest(router, http.MethodPost, base+"/chrome-mcp/enable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"session_state":"disabled"`) {
		t.Fatalf("enable body = %s", rec.Body.String())
	}
	stored, _ = sessionManager.Get(context.Background(), session.ID)
	if names := sessionMCPDisabledNames(stored); len(names) != 0 {
		t.Fatalf("disabled after enable = %#v", names)
	}

	// 全局停用的 server：会话级启用需要临时连接，当前返回 409。
	rec = doMCPAdminRequest(router, http.MethodPost, base+"/off-mcp/enable", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("off-mcp enable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 未知 server：404。
	rec = doMCPAdminRequest(router, http.MethodPost, base+"/ghost/disable", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ghost status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 未知会话：404。
	rec = doMCPAdminRequest(router, http.MethodPost, "/api/runtime/sessions/missing-session/runtime/mcps/chrome-mcp/disable", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
