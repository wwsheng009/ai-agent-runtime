package runtimeapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// fakeMCPToolsManager 只实现 ListTools，供 /mcps/{name}/tools 覆盖过滤/排序/空态。
type fakeMCPToolsManager struct{ tools []skill.ToolInfo }

func (f *fakeMCPToolsManager) FindTool(string) (skill.ToolInfo, error) {
	return skill.ToolInfo{}, fmt.Errorf("not implemented")
}

func (f *fakeMCPToolsManager) CallTool(interface{}, string, string, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (f *fakeMCPToolsManager) ListTools() []skill.ToolInfo { return f.tools }

func newMCPToolsTestRouter(t *testing.T, manager skill.MCPManager) *mux.Router {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.mcpManager = manager
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

// TestListRuntimeMCPTools_FiltersSortsAndShapes 固定 console「查看工具」面板契约：
// 只返回目标 MCP 的工具、按名称排序，并带 name/count/tools 结构。
func TestListRuntimeMCPTools_FiltersSortsAndShapes(t *testing.T) {
	router := newMCPToolsTestRouter(t, &fakeMCPToolsManager{tools: []skill.ToolInfo{
		{Name: "beta_tool", Description: "B", MCPName: "chrome-mcp", Enabled: true},
		{Name: "alpha_tool", Description: "A", MCPName: "chrome-mcp", Enabled: true},
		{Name: "zzz_other", Description: "X", MCPName: "other-mcp", Enabled: true},
	}})

	rec := doMCPAdminRequest(router, http.MethodGet, "/api/runtime/mcps/chrome-mcp/tools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"count":2`) {
		t.Fatalf("expected count 2, body = %s", body)
	}
	if !strings.Contains(body, "alpha_tool") || !strings.Contains(body, "beta_tool") {
		t.Fatalf("expected both tools, body = %s", body)
	}
	if strings.Contains(body, "zzz_other") {
		t.Fatalf("other MCP tools must be filtered out, body = %s", body)
	}
	if strings.Index(body, "alpha_tool") > strings.Index(body, "beta_tool") {
		t.Fatalf("tools should be sorted by name, body = %s", body)
	}
}

// TestListRuntimeMCPTools_EmptyAndUnavailable 覆盖空清单（未连接）与无 manager 的 503。
func TestListRuntimeMCPTools_EmptyAndUnavailable(t *testing.T) {
	router := newMCPToolsTestRouter(t, &fakeMCPToolsManager{})
	rec := doMCPAdminRequest(router, http.MethodGet, "/api/runtime/mcps/chrome-mcp/tools", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("empty manager should return 200 with count 0, got %d %s", rec.Code, rec.Body.String())
	}

	router = newMCPToolsTestRouter(t, nil)
	rec = doMCPAdminRequest(router, http.MethodGet, "/api/runtime/mcps/chrome-mcp/tools", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil manager should be 503, got %d %s", rec.Code, rec.Body.String())
	}
}
