package runtimeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

type fakeMCPAdminService struct {
	items       []mcpadmin.Item
	diagnostics *mcpadmin.ConfigDiagnostics
	addCalls    int
	updateCalls int
	removeCalls int
	reloadCalls int
	lastRequest mcpadmin.UpsertRequest
	err         error
}

func (f *fakeMCPAdminService) List(context.Context) ([]mcpadmin.Item, error) {
	return f.items, f.err
}

func (f *fakeMCPAdminService) Get(_ context.Context, name string) (*mcpconfig.MCPConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, item := range f.items {
		if item.Config.Name == name {
			cfg := item.Config
			return &cfg, nil
		}
	}
	return &mcpconfig.MCPConfig{Name: name}, nil
}

func (f *fakeMCPAdminService) Add(_ context.Context, req mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	f.addCalls++
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: req.Name, Type: req.Type, URL: req.URL}, nil
}

func (f *fakeMCPAdminService) Update(_ context.Context, name string, req mcpadmin.UpsertRequest) (*mcpconfig.MCPConfig, error) {
	f.updateCalls++
	f.lastRequest = req
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name, Type: req.Type}, nil
}

func (f *fakeMCPAdminService) Remove(context.Context, string) error {
	f.removeCalls++
	return f.err
}

func (f *fakeMCPAdminService) SetEnabled(_ context.Context, name string, enabled bool) (*mcpconfig.MCPConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mcpconfig.MCPConfig{Name: name, Enabled: enabled, Disabled: !enabled}, nil
}

func (f *fakeMCPAdminService) Reload(context.Context) error {
	f.reloadCalls++
	return f.err
}

func (f *fakeMCPAdminService) ConfigDiagnostics() *mcpadmin.ConfigDiagnostics {
	return f.diagnostics
}

func newMCPAdminTestHandler(t *testing.T, service mcpadmin.AdminService, policy MutationPolicy) *mux.Router {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetMutationPolicy(policy)
	handler.SetMCPAdminService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

func doMCPAdminRequest(router *mux.Router, method, path, body string) *httptest.ResponseRecorder {
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

func TestListRuntimeMCPs_ReturnsItems(t *testing.T) {
	service := &fakeMCPAdminService{
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
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodGet, "/api/runtime/mcps", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
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

func TestCreateRuntimeMCP_PersistsThroughService(t *testing.T) {
	service := &fakeMCPAdminService{}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps",
		`{"name":"chrome-mcp","type":"streamableHttp","url":"http://127.0.0.1:12306/mcp"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.addCalls != 1 {
		t.Fatalf("expected 1 add call, got %d", service.addCalls)
	}
	if service.lastRequest.Name != "chrome-mcp" || service.lastRequest.URL != "http://127.0.0.1:12306/mcp" {
		t.Fatalf("unexpected request: %#v", service.lastRequest)
	}
}

func TestCreateRuntimeMCP_ReadOnlyPolicyForbidden(t *testing.T) {
	service := &fakeMCPAdminService{}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{ReadOnly: true})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps",
		`{"name":"blocked","type":"stdio","command":"echo"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.addCalls != 0 {
		t.Fatalf("read-only policy must not reach the service, add calls = %d", service.addCalls)
	}
}

func TestCreateRuntimeMCP_ValidationErrorMapsTo400(t *testing.T) {
	service := &fakeMCPAdminService{err: &mcpadmin.ValidationError{Message: "invalid"}}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps", `{"name":"broken","type":"stdio"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteRuntimeMCP_NotFoundMapsTo404(t *testing.T) {
	service := &fakeMCPAdminService{err: &mcpadmin.NotFoundError{Message: "missing"}}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodDelete, "/api/runtime/mcps/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteRuntimeMCP_ErrorPath(t *testing.T) {
	service := &fakeMCPAdminService{err: context.DeadlineExceeded}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodDelete, "/api/runtime/mcps/chrome-mcp", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.removeCalls != 1 {
		t.Fatalf("expected 1 remove call, got %d", service.removeCalls)
	}
}

func TestSetRuntimeMCPEnabled_Disable(t *testing.T) {
	service := &fakeMCPAdminService{}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps/chrome-mcp/disable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("expected disabled config in body: %s", rec.Body.String())
	}
}

func TestReloadRuntimeMCPs_UsesAdminService(t *testing.T) {
	service := &fakeMCPAdminService{}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps/reload", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.reloadCalls != 1 {
		t.Fatalf("expected 1 reload call, got %d", service.reloadCalls)
	}
}

func TestReloadRuntimeMCPs_DisableReloadOpsForbidden(t *testing.T) {
	service := &fakeMCPAdminService{}
	router := newMCPAdminTestHandler(t, service, MutationPolicy{DisableReloadOps: true})

	rec := doMCPAdminRequest(router, http.MethodPost, "/api/runtime/mcps/reload", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if service.reloadCalls != 0 {
		t.Fatalf("policy must block reload, calls = %d", service.reloadCalls)
	}
}

func TestListRuntimeMCPs_Unauthorized(t *testing.T) {
	service := &fakeMCPAdminService{}
	handler := NewHandler(nil, nil, nil)
	handler.SetAdminToken("secret-token")
	handler.SetMCPAdminService(service)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/mcps", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
