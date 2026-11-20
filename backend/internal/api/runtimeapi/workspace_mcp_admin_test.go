package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpprotocol "github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// workspaceMCPAdminTestManager keeps endpoint tests isolated from real MCP
// processes while still exercising the workspace-manager cache path. The
// existing stubWorkspaceMCPAdapter is reused from session_workspace_mcp_test.go.
type workspaceMCPAdminTestManager struct {
	startCalls  int
	reloadCalls int
}

func (m *workspaceMCPAdminTestManager) LoadConfig(string) error { return nil }

func (m *workspaceMCPAdminTestManager) LoadConfigEffectiveFrom(string, string) error {
	return nil
}

func (m *workspaceMCPAdminTestManager) Start(context.Context) error {
	m.startCalls++
	return nil
}

func (m *workspaceMCPAdminTestManager) Stop() error { return nil }

func (m *workspaceMCPAdminTestManager) ListTools() []*mcpregistry.ToolInfo { return nil }

func (m *workspaceMCPAdminTestManager) CallTool(
	context.Context,
	string,
	string,
	map[string]interface{},
) (*mcpprotocol.CallToolResult, error) {
	return nil, nil
}

func (m *workspaceMCPAdminTestManager) FindTool(string) (*mcpregistry.ToolInfo, error) {
	return nil, nil
}

func (m *workspaceMCPAdminTestManager) ListResources(
	context.Context,
	string,
	*string,
) (*mcpprotocol.ListResourcesResult, error) {
	return nil, nil
}

func (m *workspaceMCPAdminTestManager) SetMCPEnabled(string, bool) error { return nil }

func (m *workspaceMCPAdminTestManager) GetMCPStatus(string) (*mcpconfig.MCPStatus, error) {
	return nil, nil
}

func (m *workspaceMCPAdminTestManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }

func (m *workspaceMCPAdminTestManager) ReloadConfig() error {
	m.reloadCalls++
	return nil
}

var _ mcpmanager.Manager = (*workspaceMCPAdminTestManager)(nil)
var _ mcpmanager.LayeredConfigLoaderFrom = (*workspaceMCPAdminTestManager)(nil)

type workspaceMCPAdminTestListResponse struct {
	Scope             string `json:"scope"`
	WorkspacePath     string `json:"workspace_path"`
	ConfigPath        string `json:"config_path"`
	Workspace         string `json:"workspace"`
	WorkspaceScoped   bool   `json:"workspace_scoped"`
	WorkspaceFallback bool   `json:"workspace_fallback"`
	Count             int    `json:"count"`
	MCPs              []struct {
		Config struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Enabled     bool   `json:"enabled"`
			Disabled    bool   `json:"disabled"`
		} `json:"config"`
		Source string `json:"source"`
	} `json:"mcps"`
}

func newWorkspaceMCPAdminEndpointTestHandler(
	t *testing.T,
	globalPath string,
) (*Handler, *mux.Router, *int, *[]*workspaceMCPAdminTestManager) {
	t.Helper()

	actorBuilds := 0
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("workspace-admin-test-token")
	handler.SetMutationPolicy(MutationPolicy{})

	createdManagers := make([]*workspaceMCPAdminTestManager, 0, 4)
	handler.SetWorkspaceMCPSupport(WorkspaceMCPSupportConfig{
		GlobalPath: globalPath,
		NewManager: func() mcpmanager.Manager {
			manager := &workspaceMCPAdminTestManager{}
			createdManagers = append(createdManagers, manager)
			return manager
		},
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			return &stubWorkspaceMCPAdapter{}
		},
	})

	// Install a hub whose factory would fail the test if any workspace endpoint
	// accidentally called GetOrCreate. The workspace API has no session input.
	handler.sessionHub = chat.NewSessionHub(func(string) (*chat.SessionActor, error) {
		actorBuilds++
		return nil, errors.New("workspace MCP endpoint must not create a session actor")
	})
	t.Cleanup(func() {
		handler.sessionHub.StopAll()
		handler.CloseWorkspaceMCPSupport()
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, router, &actorBuilds, &createdManagers
}

func workspaceMCPAdminTestURL(workspacePath, suffix string) string {
	path := "/api/runtime/workspaces/mcps" + suffix
	if strings.TrimSpace(workspacePath) == "" {
		return path
	}
	query := url.Values{}
	query.Set("workspace_path", workspacePath)
	return path + "?" + query.Encode()
}

func doWorkspaceMCPAdminTestRequest(
	router *mux.Router,
	method string,
	path string,
	body string,
	authenticated bool,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	// Avoid the implicit loopback address from granting authorization in tests
	// that intentionally omit the admin token.
	req.RemoteAddr = "203.0.113.44:4567"
	if authenticated {
		req.Header.Set("X-Skills-Admin-Token", "workspace-admin-test-token")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeWorkspaceMCPAdminTestList(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) workspaceMCPAdminTestListResponse {
	t.Helper()
	var payload workspaceMCPAdminTestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workspace MCP list: %v (body=%s)", err, rec.Body.String())
	}
	return payload
}

func TestWorkspaceMCPAdminGETWithoutSessionDoesNotCreateActor(t *testing.T) {
	workspace := t.TempDir()
	globalPath := filepath.Join(t.TempDir(), "global-mcp.yaml")
	writeWorkspaceYAML(t, globalPath, workspaceMCPServerYAML("global-only"))
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}

	handler, router, actorBuilds, _ := newWorkspaceMCPAdminEndpointTestHandler(t, globalPath)
	rec := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodGet,
		workspaceMCPAdminTestURL(workspace, ""),
		"",
		true,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeWorkspaceMCPAdminTestList(t, rec)
	wantConfigPath := filepath.Join(workspace, ".aicli", "mcp.yaml")
	if payload.Scope != "workspace" || payload.WorkspacePath != workspace || payload.Workspace != workspace {
		t.Fatalf("workspace scope = %+v, want workspace path %q", payload, workspace)
	}
	if !payload.WorkspaceScoped || !payload.WorkspaceFallback {
		t.Fatalf("scope flags = scoped:%v fallback:%v, want workspace fallback", payload.WorkspaceScoped, payload.WorkspaceFallback)
	}
	if !sameAbsPath(payload.ConfigPath, wantConfigPath) || payload.Count != 0 || len(payload.MCPs) != 0 {
		t.Fatalf("GET payload = %+v, want empty workspace target at %s", payload, wantConfigPath)
	}
	if fileExists(wantConfigPath) {
		t.Fatal("read-only GET must not create the workspace MCP file")
	}
	reloaded := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, "/reload"),
		"",
		true,
	)
	if reloaded.Code != http.StatusOK || !strings.Contains(reloaded.Body.String(), `"reloaded":false`) {
		t.Fatalf("reload without workspace config = %d/%s, want no-op", reloaded.Code, reloaded.Body.String())
	}
	if fileExists(wantConfigPath) {
		t.Fatal("reload without workspace config must not create the workspace MCP file")
	}
	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config after GET: %v", err)
	}
	if string(globalAfter) != string(globalBefore) {
		t.Fatalf("GET changed global MCP config:\nbefore=%s\nafter=%s", globalBefore, globalAfter)
	}
	if *actorBuilds != 0 || len(handler.peekSessionHub().ActiveSessionIDs(10)) != 0 {
		t.Fatalf("GET created active session actor(s): builds=%d, ids=%v", *actorBuilds, handler.peekSessionHub().ActiveSessionIDs(10))
	}
}

func TestWorkspaceMCPAdminRejectsMissingAndInvalidWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	workspaceFile := filepath.Join(root, "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	globalPath := filepath.Join(t.TempDir(), "global-mcp.yaml")
	writeWorkspaceYAML(t, globalPath, workspaceMCPServerYAML("global-only"))
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}

	handler, router, actorBuilds, _ := newWorkspaceMCPAdminEndpointTestHandler(t, globalPath)
	cases := []struct {
		name      string
		workspace string
	}{
		{name: "missing", workspace: ""},
		{name: "relative", workspace: "relative/workspace"},
		{name: "missing directory", workspace: filepath.Join(root, "does-not-exist")},
		{name: "regular file", workspace: workspaceFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				path := workspaceMCPAdminTestURL(tc.workspace, "")
				body := ""
				if method == http.MethodPost {
					body = `{"name":"must-not-write","type":"stdio","command":"echo"}`
				}
				rec := doWorkspaceMCPAdminTestRequest(router, method, path, body, true)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s status = %d, body=%s, want 400", method, rec.Code, rec.Body.String())
				}
			}
		})
	}

	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config after invalid requests: %v", err)
	}
	if string(globalAfter) != string(globalBefore) {
		t.Fatalf("invalid workspace request changed global MCP config:\nbefore=%s\nafter=%s", globalBefore, globalAfter)
	}
	if *actorBuilds != 0 || len(handler.peekSessionHub().ActiveSessionIDs(10)) != 0 {
		t.Fatalf("invalid workspace request created actor(s): builds=%d, ids=%v", *actorBuilds, handler.peekSessionHub().ActiveSessionIDs(10))
	}
}

func TestWorkspaceMCPAdminFirstWriteCRUDToggleReloadAndGlobalIsolation(t *testing.T) {
	workspace := t.TempDir()
	globalPath := filepath.Join(t.TempDir(), "global-mcp.yaml")
	writeWorkspaceYAML(t, globalPath, workspaceMCPServerYAML("global-only"))
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}

	handler, router, actorBuilds, createdManagers := newWorkspaceMCPAdminEndpointTestHandler(t, globalPath)
	workspaceConfigPath := filepath.Join(workspace, ".aicli", "mcp.yaml")
	addBody := `{"name":"workspace-demo","type":"stdio","command":"echo","args":["workspace"],"enabled":false}`

	created := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, ""),
		addBody,
		true,
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body=%s", created.Code, created.Body.String())
	}
	if !fileExists(workspaceConfigPath) {
		t.Fatalf("first write must create %s", workspaceConfigPath)
	}
	workspaceAfterAdd, err := os.ReadFile(workspaceConfigPath)
	if err != nil {
		t.Fatalf("read workspace config after add: %v", err)
	}
	if !strings.Contains(string(workspaceAfterAdd), "workspace-demo") {
		t.Fatalf("workspace config missing added MCP:\n%s", workspaceAfterAdd)
	}
	globalAfterAdd, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config after add: %v", err)
	}
	if string(globalAfterAdd) != string(globalBefore) {
		t.Fatalf("first workspace write changed global config:\nbefore=%s\nafter=%s", globalBefore, globalAfterAdd)
	}

	listed := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodGet,
		workspaceMCPAdminTestURL(workspace, ""),
		"",
		true,
	)
	if listed.Code != http.StatusOK {
		t.Fatalf("list after add status = %d, body=%s", listed.Code, listed.Body.String())
	}
	listPayload := decodeWorkspaceMCPAdminTestList(t, listed)
	if listPayload.WorkspaceFallback || !listPayload.WorkspaceScoped || listPayload.Count != 1 || len(listPayload.MCPs) != 1 {
		t.Fatalf("list scope/count after add = %+v, want one workspace MCP", listPayload)
	}
	if listPayload.MCPs[0].Config.Name != "workspace-demo" || listPayload.MCPs[0].Source != "workspace" {
		t.Fatalf("list item after add = %+v, want workspace-demo/source=workspace", listPayload.MCPs[0])
	}

	updated := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPut,
		workspaceMCPAdminTestURL(workspace, "/workspace-demo"),
		`{"description":"updated workspace MCP","command":"echo"}`,
		true,
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}
	var updatePayload struct {
		Config struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"config"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatePayload); err != nil {
		t.Fatalf("decode update payload: %v", err)
	}
	if updatePayload.Config.Name != "workspace-demo" || updatePayload.Config.Description != "updated workspace MCP" {
		t.Fatalf("update payload = %+v", updatePayload)
	}

	disabled := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, "/workspace-demo/disable"),
		"",
		true,
	)
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"enabled":false`) {
		t.Fatalf("disable response = %d/%s", disabled.Code, disabled.Body.String())
	}
	workspaceAfterDisable, err := os.ReadFile(workspaceConfigPath)
	if err != nil {
		t.Fatalf("read workspace config after disable: %v", err)
	}
	if !strings.Contains(string(workspaceAfterDisable), "enabled: false") || !strings.Contains(string(workspaceAfterDisable), "disabled: true") {
		t.Fatalf("disable was not persisted:\n%s", workspaceAfterDisable)
	}

	enabled := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, "/workspace-demo/enable"),
		"",
		true,
	)
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) {
		t.Fatalf("enable response = %d/%s", enabled.Code, enabled.Body.String())
	}
	workspaceAfterEnable, err := os.ReadFile(workspaceConfigPath)
	if err != nil {
		t.Fatalf("read workspace config after enable: %v", err)
	}
	if !strings.Contains(string(workspaceAfterEnable), "enabled: true") || !strings.Contains(string(workspaceAfterEnable), "disabled: false") {
		t.Fatalf("enable was not persisted:\n%s", workspaceAfterEnable)
	}

	reloaded := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, "/reload"),
		"",
		true,
	)
	if reloaded.Code != http.StatusOK || !strings.Contains(reloaded.Body.String(), `"reloaded":true`) {
		t.Fatalf("reload response = %d/%s", reloaded.Code, reloaded.Body.String())
	}

	removed := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodDelete,
		workspaceMCPAdminTestURL(workspace, "/workspace-demo"),
		"",
		true,
	)
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), `"removed":true`) {
		t.Fatalf("remove response = %d/%s", removed.Code, removed.Body.String())
	}
	workspaceAfterRemove, err := os.ReadFile(workspaceConfigPath)
	if err != nil {
		t.Fatalf("read workspace config after remove: %v", err)
	}
	if strings.Contains(string(workspaceAfterRemove), "workspace-demo") {
		t.Fatalf("removed MCP remains in workspace config:\n%s", workspaceAfterRemove)
	}

	finalList := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodGet,
		workspaceMCPAdminTestURL(workspace, ""),
		"",
		true,
	)
	if finalList.Code != http.StatusOK {
		t.Fatalf("final list status = %d, body=%s", finalList.Code, finalList.Body.String())
	}
	finalPayload := decodeWorkspaceMCPAdminTestList(t, finalList)
	if finalPayload.Count != 0 || len(finalPayload.MCPs) != 0 || finalPayload.WorkspaceFallback {
		t.Fatalf("final workspace list = %+v, want empty workspace scope", finalPayload)
	}
	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config at end: %v", err)
	}
	if string(globalAfter) != string(globalBefore) {
		t.Fatalf("workspace CRUD changed global config:\nbefore=%s\nafter=%s", globalBefore, globalAfter)
	}
	if len(*createdManagers) == 0 {
		t.Fatal("workspace list/mutations should exercise the workspace manager cache")
	}
	if *actorBuilds != 0 || len(handler.peekSessionHub().ActiveSessionIDs(10)) != 0 {
		t.Fatalf("workspace CRUD created actor(s): builds=%d, ids=%v", *actorBuilds, handler.peekSessionHub().ActiveSessionIDs(10))
	}
}

func TestWorkspaceMCPAdminMutationRequiresAuthorization(t *testing.T) {
	workspace := t.TempDir()
	globalPath := filepath.Join(t.TempDir(), "global-mcp.yaml")
	writeWorkspaceYAML(t, globalPath, workspaceMCPServerYAML("global-only"))
	globalBefore, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}

	handler, router, actorBuilds, _ := newWorkspaceMCPAdminEndpointTestHandler(t, globalPath)
	rec := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, ""),
		`{"name":"unauthorized","type":"stdio","command":"echo"}`,
		false,
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized mutation status = %d, body=%s, want 403", rec.Code, rec.Body.String())
	}
	if fileExists(filepath.Join(workspace, ".aicli", "mcp.yaml")) {
		t.Fatal("unauthorized mutation created workspace MCP config")
	}

	handler.SetMutationPolicy(MutationPolicy{ReadOnly: true})
	readOnlyRec := doWorkspaceMCPAdminTestRequest(
		router,
		http.MethodPost,
		workspaceMCPAdminTestURL(workspace, ""),
		`{"name":"read-only-must-not-write","type":"stdio","command":"echo"}`,
		true,
	)
	if readOnlyRec.Code != http.StatusForbidden {
		t.Fatalf("read-only mutation status = %d, body=%s, want 403", readOnlyRec.Code, readOnlyRec.Body.String())
	}
	if fileExists(filepath.Join(workspace, ".aicli", "mcp.yaml")) {
		t.Fatal("read-only mutation created workspace MCP config")
	}
	globalAfter, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global config after rejected mutations: %v", err)
	}
	if string(globalAfter) != string(globalBefore) {
		t.Fatalf("rejected mutation changed global config:\nbefore=%s\nafter=%s", globalBefore, globalAfter)
	}
	if *actorBuilds != 0 || len(handler.peekSessionHub().ActiveSessionIDs(10)) != 0 {
		t.Fatalf("rejected mutation created actor(s): builds=%d, ids=%v", *actorBuilds, handler.peekSessionHub().ActiveSessionIDs(10))
	}
}
