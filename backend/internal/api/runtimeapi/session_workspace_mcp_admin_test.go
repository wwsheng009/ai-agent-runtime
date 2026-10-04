package runtimeapi

// 会话级（工作区锚定）MCP 管理端点契约测试：
//   - 列表展示工作区实际生效的配置文件与条目（source=workspace）；
//   - 新增写入工作区 .aicli/mcp.yaml（存在时更新、缺失时创建）；
//   - scope=workspace 的启停持久化到配置文件（区别于默认的“仅本会话覆盖”）；
//   - 工作区解析与进程级同一文件时不新建 scope（避免第二份配置）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func writeWorkspaceYAML(t *testing.T, file string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
}

func workspaceMCPServerYAML(name string) string {
	return "mcpServers:\n  " + name + ":\n    name: " + name + "\n    type: stdio\n    command: npx\n    enabled: false\n    disabled: true\n"
}

func newWorkspaceMCPAdminHandler(t *testing.T, workspace string, globalPath string) (*Handler, *chat.Session, *mux.Router) {
	t.Helper()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	session, err := sessionManager.Create(context.Background(), "user-workspace-mcp-admin")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	if err := sessionManager.Update(context.Background(), session); err != nil {
		t.Fatalf("update session: %v", err)
	}

	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetMutationPolicy(MutationPolicy{})
	handler.SetSessionManager(sessionManager)
	handler.SetWorkspaceMCPSupport(WorkspaceMCPSupportConfig{
		GlobalPath: globalPath,
		NewManager: func() mcpmanager.Manager { return mcpmanager.NewManager() },
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			return &stubWorkspaceMCPAdapter{}
		},
	})
	t.Cleanup(handler.CloseWorkspaceMCPSupport)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, session, router
}

func doSessionMCPRequest(router *mux.Router, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Skills-Admin-Token", "secret-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type sessionMCPListPayload struct {
	Count    int `json:"count"`
	Disabled []string
	Scope    struct {
		Workspace         string `json:"workspace"`
		WorkspaceScoped   bool   `json:"workspace_scoped"`
		WorkspaceFallback bool   `json:"workspace_fallback"`
		Read              struct {
			Path   string `json:"path"`
			Source string `json:"source"`
			Exists bool   `json:"exists"`
		} `json:"read"`
		Write struct {
			Path   string `json:"path"`
			Source string `json:"source"`
			Exists bool   `json:"exists"`
		} `json:"write"`
	} `json:"scope"`
	MCPs []struct {
		Config struct {
			Name string `json:"name"`
		} `json:"config"`
		Source          string `json:"source"`
		SessionDisabled bool   `json:"session_disabled"`
	} `json:"mcps"`
}

func decodeSessionMCPList(t *testing.T, rec *httptest.ResponseRecorder) sessionMCPListPayload {
	t.Helper()
	var payload sessionMCPListPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list payload: %v (body=%s)", err, rec.Body.String())
	}
	return payload
}

func TestListSessionRuntimeMCPsShowsWorkspaceConfig(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))

	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	_, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	rec := doSessionMCPRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeSessionMCPList(t, rec)

	if payload.Scope.Workspace != workspace {
		t.Fatalf("scope.workspace = %q, want %q", payload.Scope.Workspace, workspace)
	}
	if !payload.Scope.WorkspaceScoped || payload.Scope.WorkspaceFallback {
		t.Fatalf("scope flags = scoped:%v fallback:%v, want scoped-only", payload.Scope.WorkspaceScoped, payload.Scope.WorkspaceFallback)
	}
	if !sameAbsPath(payload.Scope.Read.Path, workspaceFile) || payload.Scope.Read.Source != "project" || !payload.Scope.Read.Exists {
		t.Fatalf("scope.read = %+v, want workspace project file", payload.Scope.Read)
	}
	if len(payload.MCPs) != 1 || payload.MCPs[0].Config.Name != "workspace-demo" || payload.MCPs[0].Source != "workspace" {
		t.Fatalf("mcps = %+v, want workspace-demo from workspace", payload.MCPs)
	}
	if payload.Count != 0 {
		t.Fatalf("count = %d, want 0 (no session overrides)", payload.Count)
	}

	// 默认 enable/disable 仍是会话覆盖（不写文件）。
	toggle := doSessionMCPRequest(router, http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/disable", "")
	if toggle.Code != http.StatusOK {
		t.Fatalf("session-only disable status = %d, body=%s", toggle.Code, toggle.Body.String())
	}
	body, err := os.ReadFile(workspaceFile)
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if strings.Contains(string(body), "runtime_mcp_session_disabled") {
		t.Fatalf("session-only toggle must not write the config file:\n%s", body)
	}
	after := decodeSessionMCPList(t, doSessionMCPRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", ""))
	if len(after.Disabled) != 1 || after.Disabled[0] != "workspace-demo" {
		t.Fatalf("disabled = %v, want [workspace-demo]", after.Disabled)
	}
	if !after.MCPs[0].SessionDisabled {
		t.Fatal("session_disabled must be true after session-only disable")
	}
}

func TestSessionMCPAddWritesWorkspaceFile(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))

	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	_, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	body := `{"name":"added-demo","type":"stdio","command":"npx","args":["-y","demo"],"enabled":false}`
	rec := doSessionMCPRequest(router, http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body=%s", rec.Code, rec.Body.String())
	}
	written, err := os.ReadFile(workspaceFile)
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if !strings.Contains(string(written), "added-demo") {
		t.Fatalf("workspace file must contain added server:\n%s", written)
	}

	payload := decodeSessionMCPList(t, doSessionMCPRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", ""))
	names := make([]string, 0, len(payload.MCPs))
	for _, item := range payload.MCPs {
		names = append(names, item.Config.Name)
	}
	if len(names) != 2 || payload.MCPs[0].Source != "workspace" {
		t.Fatalf("mcps after add = %v (source=%v), want [added-demo workspace-demo]", names, payload.MCPs[0].Source)
	}

	// scope=workspace 启停：持久化写文件。
	disable := doSessionMCPRequest(router, http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/mcps/added-demo/disable?scope=workspace", "")
	if disable.Code != http.StatusOK {
		t.Fatalf("persisted disable status = %d, body=%s", disable.Code, disable.Body.String())
	}
	var togglePayload struct {
		Scope      string `json:"scope"`
		ConfigPath string `json:"config_path"`
	}
	if err := json.Unmarshal(disable.Body.Bytes(), &togglePayload); err != nil {
		t.Fatalf("decode toggle payload: %v", err)
	}
	if togglePayload.Scope != "workspace" || !sameAbsPath(togglePayload.ConfigPath, workspaceFile) {
		t.Fatalf("toggle payload = %+v, want workspace scope at %s", togglePayload, workspaceFile)
	}

	// 删除同样落工作区文件。
	remove := doSessionMCPRequest(router, http.MethodDelete, "/api/runtime/sessions/"+session.ID+"/runtime/mcps/added-demo", "")
	if remove.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body=%s", remove.Code, remove.Body.String())
	}
	writtenAfter, _ := os.ReadFile(workspaceFile)
	if strings.Contains(string(writtenAfter), "added-demo") {
		t.Fatalf("removed server must disappear from workspace file:\n%s", writtenAfter)
	}
}

func TestSessionMCPAddCreatesWorkspaceFileWhenMissing(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	handler, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	// 让回退分支可读：全局管理面注入空配置服务。
	handler.SetMCPAdminService(mcpadmin.NewService(globalPath, mcpadmin.WithApplyOnMutate(false)))

	payload := decodeSessionMCPList(t, doSessionMCPRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", ""))
	if !payload.Scope.WorkspaceScoped || !payload.Scope.WorkspaceFallback {
		t.Fatalf("scope = %+v, want workspace fallback before first add", payload.Scope)
	}
	if !sameAbsPath(payload.Scope.Write.Path, workspaceFile) {
		t.Fatalf("write path = %q, want %q", payload.Scope.Write.Path, workspaceFile)
	}

	body := `{"name":"fresh-demo","type":"stdio","command":"npx","enabled":false}`
	rec := doSessionMCPRequest(router, http.MethodPost, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !fileExists(workspaceFile) {
		t.Fatalf("add must create %s", workspaceFile)
	}

	// 落盘后解析即命中工作区文件：列表切到 workspace 来源（不再回退）。
	after := decodeSessionMCPList(t, doSessionMCPRequest(router, http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime/mcps", ""))
	if after.Scope.WorkspaceFallback {
		t.Fatalf("workspace file must win after first add: %+v", after.Scope)
	}
	if len(after.MCPs) != 1 || after.MCPs[0].Config.Name != "fresh-demo" || after.MCPs[0].Source != "workspace" {
		t.Fatalf("mcps after create = %+v, want fresh-demo from workspace", after.MCPs)
	}
}

func TestSessionMCPAdminScopeReusesGlobalWhenSameFile(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))

	// 进程级解析就是同一个文件：不得再声明 workspace scope（避免第二份配置）。
	handler, session, _ := newWorkspaceMCPAdminHandler(t, workspace, workspaceFile)
	scope := handler.sessionMCPAdminScopeFor(session)
	if scope.WorkspaceScoped {
		t.Fatalf("scope must reuse the process-level file, got %+v", scope)
	}
	if !sameAbsPath(scope.WritePath, workspaceFile) {
		t.Fatalf("write path = %q, want global file %q", scope.WritePath, workspaceFile)
	}
}
