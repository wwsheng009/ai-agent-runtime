package runtimeapi

// 会话级「临时启用」契约测试：
//   - 配置停用的 server：enable（默认 scope=session）→ 200 + session_state=enabled，
//     名单落会话元数据、列表返回 enabled / session_enabled；
//   - 工具面合并：sessionScopedMCPSurface 暴露临时连接的工具并路由 CallTool；
//   - disable 回收临时连接；构建失败 → 503 且不落名单。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeTempMCPAdapter struct {
	mu     sync.Mutex
	tools  []skill.ToolInfo
	calls  int
	closed bool
}

func (f *fakeTempMCPAdapter) ListTools() []skill.ToolInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]skill.ToolInfo, len(f.tools))
	copy(out, f.tools)
	return out
}

func (f *fakeTempMCPAdapter) FindTool(name string) (skill.ToolInfo, error) {
	for _, info := range f.ListTools() {
		if strings.EqualFold(strings.TrimSpace(info.Name), strings.TrimSpace(name)) {
			return info, nil
		}
	}
	return skill.ToolInfo{}, fmt.Errorf("tool %q not found", name)
}

func (f *fakeTempMCPAdapter) CallTool(_ interface{}, _, _ string, _ map[string]interface{}) (interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return "temp-ok", nil
}

func (f *fakeTempMCPAdapter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// installFakeSessionMCPTempBuilder 替换临时连接构建器；返回 close 计数器。
func installFakeSessionMCPTempBuilder(t *testing.T, adapter skill.MCPManager, buildErr error) *int {
	t.Helper()
	closes := 0
	old := sessionMCPTempBuilder
	sessionMCPTempBuilder = func(_ *Handler, _ *chat.Session, names []string) (*sessionMCPTempBuild, error) {
		if buildErr != nil {
			return nil, buildErr
		}
		return &sessionMCPTempBuild{
			adapter: adapter,
			names:   normalizeSessionMCPNames(names),
			close:   func() { closes++ },
		}, nil
	}
	t.Cleanup(func() { sessionMCPTempBuilder = old })
	return &closes
}

func TestSessionMCPTempEnableSurfacesToolsAndDisableReclaims(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	handler, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	tempAdapter := &fakeTempMCPAdapter{tools: []skill.ToolInfo{{
		Name:    "demo_tool",
		MCPName: "workspace-demo",
		Enabled: true,
	}}}
	closes := installFakeSessionMCPTempBuilder(t, tempAdapter, nil)

	// 1) 本会话启用：配置里 enabled=false，走临时连接（不写配置文件）。
	rec := doSessionMCPRequest(router, http.MethodPost,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/enable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("temp enable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var toggle struct {
		Scope        string `json:"scope"`
		SessionState string `json:"session_state"`
		Changed      bool   `json:"changed"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &toggle); err != nil {
		t.Fatalf("decode toggle: %v", err)
	}
	if toggle.Scope != "session" || toggle.SessionState != "enabled" || !toggle.Changed {
		t.Fatalf("toggle = %+v, want session/enabled/changed", toggle)
	}
	if !strings.Contains(toggle.Message, "临时启用") {
		t.Fatalf("message = %q, want 临时启用", toggle.Message)
	}
	reloaded := handler.sessionByID(context.Background(), session.ID)
	if names := sessionMCPEnabledNames(reloaded); len(names) != 1 || names[0] != "workspace-demo" {
		t.Fatalf("session temp enabled names = %v, want [workspace-demo]", names)
	}

	// 2) 列表：enabled 名单 + 条目标记。
	rec = doSessionMCPRequest(router, http.MethodGet,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Enabled []string `json:"enabled"`
		MCPs    []struct {
			Config struct {
				Name string `json:"name"`
			} `json:"config"`
			SessionEnabled bool `json:"session_enabled"`
		} `json:"mcps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Enabled) != 1 || list.Enabled[0] != "workspace-demo" {
		t.Fatalf("enabled = %v, want [workspace-demo]", list.Enabled)
	}
	found := false
	for _, item := range list.MCPs {
		if item.Config.Name == "workspace-demo" {
			found = true
			if !item.SessionEnabled {
				t.Fatalf("entry session_enabled = false, want true")
			}
		}
	}
	if !found {
		t.Fatalf("workspace-demo missing from list: %s", rec.Body.String())
	}

	// 3) 工具面：base（stub）没有工具，临时连接的 demo_tool 必须可见且可调用。
	surface := handler.sessionScopedMCPSurface(context.Background(), session.ID, &stubWorkspaceMCPAdapter{})
	tools := surface.ListTools()
	visible := false
	for _, info := range tools {
		if info.Name == "demo_tool" && info.MCPName == "workspace-demo" {
			visible = true
		}
	}
	if !visible {
		t.Fatalf("temp tool not visible on session surface: %+v", tools)
	}
	if _, err := surface.CallTool(context.Background(), "workspace-demo", "demo_tool", nil); err != nil {
		t.Fatalf("CallTool via temp runtime: %v", err)
	}
	if tempAdapter.callCount() == 0 {
		t.Fatal("CallTool must route to the temp runtime")
	}

	// 4) 本会话停用：回收临时连接，工具面恢复为空。
	rec = doSessionMCPRequest(router, http.MethodPost,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/disable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("temp disable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &toggle); err != nil {
		t.Fatalf("decode disable: %v", err)
	}
	if toggle.SessionState != "disabled" || !toggle.Changed {
		t.Fatalf("disable toggle = %+v, want disabled/changed", toggle)
	}
	reloaded = handler.sessionByID(context.Background(), session.ID)
	if names := sessionMCPEnabledNames(reloaded); len(names) != 0 {
		t.Fatalf("temp enabled names after disable = %v, want empty", names)
	}
	if *closes == 0 {
		t.Fatal("temp runtime must be closed on session-level disable")
	}
	surface = handler.sessionScopedMCPSurface(context.Background(), session.ID, &stubWorkspaceMCPAdapter{})
	if len(surface.ListTools()) != 0 {
		t.Fatalf("temp tool still visible after disable: %+v", surface.ListTools())
	}
}

func TestSessionMCPTempEnableBuildFailureKeepsStateClean(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	handler, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	installFakeSessionMCPTempBuilder(t, nil, fmt.Errorf("boom"))

	rec := doSessionMCPRequest(router, http.MethodPost,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/enable", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "临时启用") && !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("error body = %s, want cause", rec.Body.String())
	}
	reloaded := handler.sessionByID(context.Background(), session.ID)
	if names := sessionMCPEnabledNames(reloaded); len(names) != 0 {
		t.Fatalf("failed enable must not persist names, got %v", names)
	}
}

// 工具清单端点必须合并「本会话临时启用」的私有连接工具（配置停用、仅本会话
// 可见）：endpoint 走底层 manager 时看不到临时连接，需按会话覆盖视图补齐。
func TestSessionMCPTempToolsEndpointListsTempTools(t *testing.T) {
	workspace := t.TempDir()
	workspaceFile := filepath.Join(workspace, ".aicli", "mcp.yaml")
	writeWorkspaceYAML(t, workspaceFile, workspaceMCPServerYAML("workspace-demo"))
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	writeWorkspaceYAML(t, globalPath, "mcpServers: {}\n")

	_, session, router := newWorkspaceMCPAdminHandler(t, workspace, globalPath)
	tempAdapter := &fakeTempMCPAdapter{tools: []skill.ToolInfo{{
		Name:        "demo_tool",
		MCPName:     "workspace-demo",
		Description: "e2e probe tool",
		Enabled:     true,
	}}}
	installFakeSessionMCPTempBuilder(t, tempAdapter, nil)

	rec := doSessionMCPRequest(router, http.MethodPost,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/enable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("temp enable status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = doSessionMCPRequest(router, http.MethodGet,
		"/api/runtime/sessions/"+session.ID+"/runtime/mcps/workspace-demo/tools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tools status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Count int `json:"count"`
		Tools []struct {
			Name              string `json:"name"`
			Enabled           bool   `json:"enabled"`
			ConfiguredEnabled bool   `json:"configured_enabled"`
			Healthy           bool   `json:"healthy"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if payload.Count != 1 || len(payload.Tools) != 1 || payload.Tools[0].Name != "demo_tool" {
		t.Fatalf("tools = %+v, want [demo_tool]", payload)
	}
	if payload.Tools[0].ConfiguredEnabled {
		t.Fatal("temp tool must report configured_enabled=false")
	}
	if !payload.Tools[0].Enabled {
		t.Fatal("temp tool must be enabled in the session surface")
	}
}
