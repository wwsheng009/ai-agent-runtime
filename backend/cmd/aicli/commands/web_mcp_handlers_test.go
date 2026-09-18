package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type fakeWebMCPAdmin struct {
	items       []mcpadmin.Item
	diagnostics *mcpadmin.ConfigDiagnostics
	addCalls    int
	updateCalls int
	removeCalls int
	reloadCalls int
	lastRequest mcpadmin.UpsertRequest
	err         error

	toolToggleNames  []string
	toolToggleStates []bool
	bulkToggleTools  [][]string
	bulkToggleStates []bool
}

func (f *fakeWebMCPAdmin) List(context.Context) ([]mcpadmin.Item, error) {
	return f.items, f.err
}

func (f *fakeWebMCPAdmin) Get(_ context.Context, name string) (*mcpconfig.MCPConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name}, nil
}

func (f *fakeWebMCPAdmin) Add(_ context.Context, req mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	f.addCalls++
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: req.Name, Type: req.Type, URL: req.URL, Enabled: true}, nil
}

func (f *fakeWebMCPAdmin) Update(_ context.Context, name string, req mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	f.updateCalls++
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name, Type: req.Type}, nil
}

func (f *fakeWebMCPAdmin) Remove(context.Context, string) error {
	f.removeCalls++
	return f.err
}

func (f *fakeWebMCPAdmin) SetEnabled(_ context.Context, name string, enabled bool) (*mcpconfig.MCPConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name, Enabled: enabled, Disabled: !enabled}, nil
}

func (f *fakeWebMCPAdmin) Reload(context.Context) error {
	f.reloadCalls++
	return f.err
}

func (f *fakeWebMCPAdmin) ConfigDiagnostics() *mcpadmin.ConfigDiagnostics {
	return f.diagnostics
}

func (f *fakeWebMCPAdmin) SetToolEnabled(_ context.Context, name, tool string, enabled bool) (*mcpconfig.MCPConfig, error) {
	f.toolToggleNames = append(f.toolToggleNames, tool)
	f.toolToggleStates = append(f.toolToggleStates, enabled)
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name}, nil
}

func (f *fakeWebMCPAdmin) SetToolsEnabled(_ context.Context, name string, tools []string, enabled bool) (*mcpconfig.MCPConfig, error) {
	f.bulkToggleTools = append(f.bulkToggleTools, tools)
	f.bulkToggleStates = append(f.bulkToggleStates, enabled)
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name}, nil
}

func withFakeWebMCPAdmin(t *testing.T, fake mcpadmin.AdminService) {
	t.Helper()
	prev := chatWebMCPAdminServiceFn
	chatWebMCPAdminServiceFn = func() (mcpadmin.AdminService, error) { return fake, nil }
	t.Cleanup(func() { chatWebMCPAdminServiceFn = prev })
}

func doWebMCPRequest(t *testing.T, handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestHandleChatWebAPIMCPs_List(t *testing.T) {
	fake := &fakeWebMCPAdmin{
		items: []mcpadmin.Item{{
			Config: mcpconfig.MCPConfig{Name: "chrome-mcp", Type: "streamable", URL: "http://127.0.0.1:12306/mcp", Enabled: true},
			Status: &mcpconfig.MCPStatus{Name: "chrome-mcp", Enabled: true, Connected: true, ToolCount: 27},
		}},
		diagnostics: &mcpadmin.ConfigDiagnostics{
			Path:          "E:/repo/.aicli/mcp.yaml",
			Source:        "project",
			Exists:        true,
			ManagerLoaded: true,
		},
	}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCPs, http.MethodGet, ChatWebAPIMCPsPath, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"chrome-mcp", "streamable", "27",
		`"config"`, "E:/repo/.aicli/mcp.yaml", `"source":"project"`,
		`"summary"`, `"total":1`, `"enabled":1`, `"connected":1`, `"tools":27`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func TestHandleChatWebAPIMCPs_Create(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCPs, http.MethodPost, ChatWebAPIMCPsPath,
		`{"name":"chrome-mcp","type":"streamableHttp","url":"http://127.0.0.1:12306/mcp"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.addCalls != 1 || fake.lastRequest.Name != "chrome-mcp" {
		t.Fatalf("unexpected add: calls=%d request=%#v", fake.addCalls, fake.lastRequest)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := payload["config"]; !ok {
		t.Fatalf("response missing config: %s", recorder.Body.String())
	}
}

func TestHandleChatWebAPIMCPs_MethodNotAllowed(t *testing.T) {
	withFakeWebMCPAdmin(t, &fakeWebMCPAdmin{})
	recorder := doWebMCPRequest(t, HandleChatWebAPIMCPs, http.MethodDelete, ChatWebAPIMCPsPath, "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestHandleChatWebAPIMCP_UpdateAndDelete(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPut, ChatWebAPIMCPsPath+"/chrome-mcp",
		`{"description":"浏览器 MCP"}`)
	if recorder.Code != http.StatusOK || fake.updateCalls != 1 {
		t.Fatalf("update: status=%d calls=%d body=%s", recorder.Code, fake.updateCalls, recorder.Body.String())
	}
	if fake.lastRequest.Description == nil || *fake.lastRequest.Description != "浏览器 MCP" {
		t.Fatalf("unexpected update request: %#v", fake.lastRequest)
	}

	recorder = doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodDelete, ChatWebAPIMCPsPath+"/chrome-mcp", "")
	if recorder.Code != http.StatusOK || fake.removeCalls != 1 {
		t.Fatalf("delete: status=%d calls=%d body=%s", recorder.Code, fake.removeCalls, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"removed":true`) {
		t.Fatalf("unexpected delete body: %s", recorder.Body.String())
	}
}

func TestHandleChatWebAPIMCP_EnableDisable(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost, ChatWebAPIMCPsPath+"/chrome-mcp/disable", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"enabled":false`) {
		t.Fatalf("expected disabled config: %s", recorder.Body.String())
	}

	recorder = doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost, ChatWebAPIMCPsPath+"/chrome-mcp/enable", "")
	if !strings.Contains(recorder.Body.String(), `"enabled":true`) {
		t.Fatalf("expected enabled config: %s", recorder.Body.String())
	}
}

func TestHandleChatWebAPIMCP_Reload(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost, ChatWebAPIMCPsPath+"/reload", "")
	if recorder.Code != http.StatusOK || fake.reloadCalls != 1 {
		t.Fatalf("reload: status=%d calls=%d body=%s", recorder.Code, fake.reloadCalls, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"reloaded":true`) {
		t.Fatalf("unexpected reload body: %s", recorder.Body.String())
	}
}

func TestHandleChatWebAPIMCP_InvalidPath(t *testing.T) {
	withFakeWebMCPAdmin(t, &fakeWebMCPAdmin{})
	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodGet, ChatWebAPIMCPsPath+"/a/b/c", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHandleChatWebAPIMCPs_ValidationErrorMapsTo400(t *testing.T) {
	fake := &fakeWebMCPAdmin{err: &mcpadmin.ValidationError{Message: "bad request"}}
	withFakeWebMCPAdmin(t, fake)
	recorder := doWebMCPRequest(t, HandleChatWebAPIMCPs, http.MethodPost, ChatWebAPIMCPsPath,
		`{"name":"broken","type":"stdio"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleChatWebAPIMCP_NotFoundMapsTo404(t *testing.T) {
	fake := &fakeWebMCPAdmin{err: &mcpadmin.NotFoundError{Message: "missing"}}
	withFakeWebMCPAdmin(t, fake)
	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPut, ChatWebAPIMCPsPath+"/nope", `{}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
	}
}

// TestChatWebPage_ServesMCPAssets 确认 MCP 页签的前端资源已被 go:embed 收录并可被
// 页面服务器正确伺服（新增 js 文件时最容易漏的回归点）。
func TestChatWebPage_ServesMCPAssets(t *testing.T) {
	script := httptest.NewRequest(http.MethodGet, "/web/js/mcp.js", nil)
	scriptRecorder := httptest.NewRecorder()
	HandleChatWebPage(scriptRecorder, script)
	if scriptRecorder.Code != http.StatusOK {
		t.Fatalf("GET /web/js/mcp.js status = %d", scriptRecorder.Code)
	}
	if contentType := scriptRecorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("mcp.js content-type = %q", contentType)
	}
	if !strings.Contains(scriptRecorder.Body.String(), "loadMCPs") {
		t.Fatalf("mcp.js body missing loadMCPs export")
	}

	page := httptest.NewRequest(http.MethodGet, "/web/", nil)
	pageRecorder := httptest.NewRecorder()
	HandleChatWebPage(pageRecorder, page)
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("GET /web/ status = %d", pageRecorder.Code)
	}
	body := pageRecorder.Body.String()
	for _, want := range []string{"tab-mcp-btn", "tab-mcp", "mcp-list"} {
		if !strings.Contains(body, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
}

// stubMCPToolLister 供工具清单端点测试注入（生产实现是进程内 MCPManagerInstance）。
type stubMCPToolLister struct {
	tools []*mcpregistry.ToolInfo
}

func (s stubMCPToolLister) ListTools() []*mcpregistry.ToolInfo { return s.tools }

func TestHandleChatWebAPIMCP_ToolToggle(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost,
		ChatWebAPIMCPsPath+"/chrome-mcp/tools/list_pages/disable", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.toolToggleNames) != 1 || fake.toolToggleNames[0] != "list_pages" {
		t.Fatalf("unexpected tool toggle names: %#v", fake.toolToggleNames)
	}
	if len(fake.toolToggleStates) != 1 || fake.toolToggleStates[0] {
		t.Fatalf("unexpected tool toggle states: %#v", fake.toolToggleStates)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("body missing disabled state: %s", body)
	}
}

func TestHandleChatWebAPIMCP_ToolsBulkToggle(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodPost,
		ChatWebAPIMCPsPath+"/chrome-mcp/tools/enable", `{"tools":["navigate_page","list_pages"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.bulkToggleTools) != 1 || len(fake.bulkToggleTools[0]) != 2 {
		t.Fatalf("unexpected bulk toggle tools: %#v", fake.bulkToggleTools)
	}
	if len(fake.bulkToggleStates) != 1 || !fake.bulkToggleStates[0] {
		t.Fatalf("unexpected bulk toggle states: %#v", fake.bulkToggleStates)
	}
}

func TestHandleChatWebAPIMCP_ToolsListIncludesDisabled(t *testing.T) {
	fake := &fakeWebMCPAdmin{}
	withFakeWebMCPAdmin(t, fake)

	prev := chatWebMCPToolListerFn
	chatWebMCPToolListerFn = func() mcpToolsLister {
		return stubMCPToolLister{tools: []*mcpregistry.ToolInfo{
			{
				MCPName:      "chrome-mcp",
				Enabled:      true,
				UserDisabled: true,
				Tool:         &protocol.Tool{Name: "list_pages"},
			},
			{
				MCPName: "chrome-mcp",
				Enabled: true,
				Tool:    &protocol.Tool{Name: "navigate_page"},
			},
		}}
	}
	t.Cleanup(func() { chatWebMCPToolListerFn = prev })

	recorder := doWebMCPRequest(t, HandleChatWebAPIMCP, http.MethodGet,
		ChatWebAPIMCPsPath+"/chrome-mcp/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"list_pages"`,
		`"configured_enabled":false`,
		`"enabled":false`,
		`"navigate_page"`,
		`"configured_enabled":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("tools body missing %q: %s", want, body)
		}
	}
}
