package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type fakeManager struct {
	mu          sync.Mutex
	loadPath    string
	loadCalls   int
	reloadCalls int
	startCalls  int
	statuses    []*config.MCPStatus
	loadErr     error
}

func (f *fakeManager) LoadConfig(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls++
	f.loadPath = path
	return f.loadErr
}

func (f *fakeManager) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return nil
}

func (f *fakeManager) Stop() error { return nil }

func (f *fakeManager) ListTools() []*registry.ToolInfo { return nil }

func (f *fakeManager) CallTool(context.Context, string, string, map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, nil
}

func (f *fakeManager) FindTool(string) (*registry.ToolInfo, error) { return nil, nil }

func (f *fakeManager) ListResources(context.Context, string, *string) (*protocol.ListResourcesResult, error) {
	return nil, nil
}

func (f *fakeManager) SetMCPEnabled(string, bool) error { return nil }

func (f *fakeManager) GetMCPStatus(string) (*config.MCPStatus, error) { return nil, nil }

func (f *fakeManager) ListMCPs() []*config.MCPStatus { return f.statuses }

func (f *fakeManager) ReloadConfig() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadCalls++
	return nil
}

var _ manager.Manager = (*fakeManager)(nil)

func newTestService(t *testing.T) (*Service, *fakeManager, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")
	fake := &fakeManager{}
	service := NewService(configPath, WithManager(fake))
	return service, fake, configPath
}

func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func TestService_AddPersistsAndApplies(t *testing.T) {
	service, fake, configPath := newTestService(t)
	refreshed := 0
	service.refresh = func() { refreshed++ }

	cfg, err := service.Add(context.Background(), UpsertRequest{
		Name: "chrome-mcp",
		Type: "streamableHttp",
		URL:  "http://127.0.0.1:12306/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer test",
		},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if cfg.Type != "streamable" {
		t.Fatalf("expected canonical type streamable, got %q", cfg.Type)
	}
	if cfg.Env["HEADER_Authorization"] != "Bearer test" {
		t.Fatalf("expected header env mapping, got %#v", cfg.Env)
	}

	content := readConfigFile(t, configPath)
	for _, want := range []string{"chrome-mcp", "streamable", "127.0.0.1:12306"} {
		if !strings.Contains(content, want) {
			t.Fatalf("config file missing %q:\n%s", want, content)
		}
	}
	if fake.reloadCalls != 1 || fake.startCalls != 1 || refreshed != 1 {
		t.Fatalf("apply calls: reload=%d start=%d refresh=%d", fake.reloadCalls, fake.startCalls, refreshed)
	}
}

func TestService_AddRejectsDuplicate(t *testing.T) {
	service, _, _ := newTestService(t)
	req := UpsertRequest{Name: "dup", Type: "stdio", Command: "echo"}
	if _, err := service.Add(context.Background(), req); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := service.Add(context.Background(), req); err == nil {
		t.Fatal("expected duplicate add to fail")
	}
}

func TestService_SetEnabledPersists(t *testing.T) {
	service, fake, configPath := newTestService(t)
	if _, err := service.Add(context.Background(), UpsertRequest{
		Name: "local", Type: "stdio", Command: "echo",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	cfg, err := service.SetEnabled(context.Background(), "local", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if cfg.Enabled || !cfg.Disabled {
		t.Fatalf("expected disabled config, got enabled=%v disabled=%v", cfg.Enabled, cfg.Disabled)
	}
	content := readConfigFile(t, configPath)
	if !strings.Contains(content, "enabled: false") || !strings.Contains(content, "disabled: true") {
		t.Fatalf("config file not persisted disable state:\n%s", content)
	}
	if fake.reloadCalls != 2 || fake.startCalls != 2 {
		t.Fatalf("expected apply after add+disable, got reload=%d start=%d", fake.reloadCalls, fake.startCalls)
	}
}

func TestService_UpdateMergesFields(t *testing.T) {
	service, _, _ := newTestService(t)
	if _, err := service.Add(context.Background(), UpsertRequest{
		Name: "chrome-mcp", Type: "streamable", URL: "http://127.0.0.1:12306/mcp",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	description := "浏览器 MCP"
	cfg, err := service.Update(context.Background(), "chrome-mcp", UpsertRequest{Description: &description})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if cfg.Description != description {
		t.Fatalf("description not updated: %q", cfg.Description)
	}
	if cfg.Type != "streamable" || cfg.URL != "http://127.0.0.1:12306/mcp" {
		t.Fatalf("update must preserve transport fields, got type=%q url=%q", cfg.Type, cfg.URL)
	}
	if !cfg.IsEnabled() {
		t.Fatal("update must preserve enabled state")
	}
}

func TestService_Remove(t *testing.T) {
	service, _, configPath := newTestService(t)
	if _, err := service.Add(context.Background(), UpsertRequest{
		Name: "to-remove", Type: "stdio", Command: "echo",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := service.Remove(context.Background(), "to-remove"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if content := readConfigFile(t, configPath); strings.Contains(content, "to-remove") {
		t.Fatalf("config still contains removed entry:\n%s", content)
	}
	if err := service.Remove(context.Background(), "to-remove"); err == nil {
		t.Fatal("expected removing missing entry to fail")
	}
}

func TestService_ListMergesStatus(t *testing.T) {
	service, fake, _ := newTestService(t)
	if _, err := service.Add(context.Background(), UpsertRequest{
		Name: "chrome-mcp", Type: "streamable", URL: "http://127.0.0.1:12306/mcp",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	fake.statuses = []*config.MCPStatus{{
		Name: "chrome-mcp", Type: "streamable", Enabled: true, Connected: true, ToolCount: 27,
	}}

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Status == nil || !items[0].Status.Connected || items[0].Status.ToolCount != 27 {
		t.Fatalf("unexpected status: %#v", items[0].Status)
	}
}

func TestService_EnsureManagerLazily(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nested", "mcp.yaml")
	fake := &fakeManager{}
	service := NewService(configPath, WithManagerFactory(func() manager.Manager { return fake }))

	if _, err := service.List(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}
	if fake.loadCalls != 1 || fake.loadPath != configPath {
		t.Fatalf("expected lazy LoadConfig, got calls=%d path=%q", fake.loadCalls, fake.loadPath)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config file auto-created: %v", err)
	}
}

func TestBuildConfig_Validation(t *testing.T) {
	cases := []struct {
		name string
		req  UpsertRequest
	}{
		{name: "missing name", req: UpsertRequest{Type: "stdio", Command: "echo"}},
		{name: "stdio without command", req: UpsertRequest{Name: "a", Type: "stdio"}},
		{name: "url transport without url", req: UpsertRequest{Name: "a", Type: "streamable"}},
		{name: "unsupported type", req: UpsertRequest{Name: "a", Type: "carrier-pigeon", URL: "http://x"}},
		{name: "bad trust level", req: UpsertRequest{Name: "a", Type: "stdio", Command: "echo", TrustLevel: "root"}},
	}
	for _, tc := range cases {
		if _, err := BuildConfig(tc.req, nil); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
}

func TestNormalizeTransportType(t *testing.T) {
	cases := map[string]string{
		"streamableHttp":  "streamable",
		"streamable-http": "streamable",
		"streamable_http": "streamable",
		"http":            "streamable",
		"ws":              "websocket",
		"SSE":             "sse",
	}
	for input, want := range cases {
		if got := NormalizeTransportType(input); got != want {
			t.Fatalf("NormalizeTransportType(%q) = %q, want %q", input, got, want)
		}
	}
}
