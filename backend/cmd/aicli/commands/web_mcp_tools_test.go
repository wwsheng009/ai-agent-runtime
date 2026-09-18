package commands

import (
	"net/http"
	"strings"
	"testing"

	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// fakeMCPToolsLister 供 /web/api/mcps/{name}/tools 覆盖过滤/排序/空态。
type fakeMCPToolsLister struct{ tools []*mcpregistry.ToolInfo }

func (f *fakeMCPToolsLister) ListTools() []*mcpregistry.ToolInfo { return f.tools }

// TestHandleChatWebAPIMCP_Tools 固定微型 Web「工具」按钮的接口契约：
// GET 返回目标 MCP 的工具（按名称排序），其他方法 405，空数据源返回 count=0。
func TestHandleChatWebAPIMCP_Tools(t *testing.T) {
	previous := chatWebMCPToolListerFn
	t.Cleanup(func() { chatWebMCPToolListerFn = previous })
	chatWebMCPToolListerFn = func() mcpToolsLister {
		return &fakeMCPToolsLister{tools: []*mcpregistry.ToolInfo{
			{MCPName: "chrome-devtools", Enabled: true, Tool: &mcpprotocol.Tool{Name: "beta_tool", Description: "B"}},
			{MCPName: "other-mcp", Enabled: true, Tool: &mcpprotocol.Tool{Name: "zzz_other", Description: "X"}},
			{MCPName: "chrome-devtools", Enabled: true, Tool: &mcpprotocol.Tool{Name: "alpha_tool", Description: "A"}},
		}}
	}

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodGet, ChatWebAPIMCPsPath+"/chrome-devtools/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
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

	recorder = doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost, ChatWebAPIMCPsPath+"/chrome-devtools/tools", "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /tools should be 405, got %d %s", recorder.Code, recorder.Body.String())
	}

	chatWebMCPToolListerFn = func() mcpToolsLister { return nil }
	recorder = doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodGet, ChatWebAPIMCPsPath+"/chrome-devtools/tools", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":0`) {
		t.Fatalf("nil lister should return 200 with count 0, got %d %s", recorder.Code, recorder.Body.String())
	}
}
