package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/config"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/protocol"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/registry"
	runtimecfg "github.com/ai-gateway/ai-agent-runtime/internal/config"
)

func TestNewDefaultManagerWithRuntimeConfig_AppliesSandboxToLocalTools(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sandbox.Enabled = true
	cfg.Sandbox.ReadOnlyPaths = []string{tmpDir}

	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	_, err := manager.Execute(context.Background(), "write", map[string]interface{}{
		"file_path": filepath.Join(tmpDir, "blocked.txt"),
		"content":   "sandbox",
	})
	if err == nil {
		t.Fatal("expected sandbox denial, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("expected read-only sandbox error, got %v", err)
	}
}

func TestNewDefaultManagerWithRuntimeConfig_TodosFallbacksToMemoryUnderSandbox(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sandbox.Enabled = true
	cfg.Sandbox.AllowedPaths = []string{tmpDir}
	cfg.Sandbox.ReadOnlyPaths = []string{tmpDir}

	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	output, err := manager.Execute(context.Background(), "todos", map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{
				"content":     "Task 1",
				"status":      "pending",
				"active_form": "Doing Task 1",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected todos to fallback to memory, got %v", err)
	}
	if !strings.Contains(output, "任务列表已更新") {
		t.Fatalf("unexpected todos output: %s", output)
	}
}

func TestAgentAdapter_ListTools_MergesToolkitAndMCP(t *testing.T) {
	manager := NewDefaultManager(newStubMCPManager())
	adapter := NewAgentAdapter(manager)

	tools := adapter.ListTools()
	if len(tools) == 0 {
		t.Fatal("expected adapter to expose tools")
	}

	toolNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		toolNames[tool.Name] = struct{}{}
	}
	if _, ok := toolNames["bash"]; !ok {
		t.Fatalf("expected toolkit tool bash to be present: %+v", tools)
	}
	if _, ok := toolNames["mcp_echo"]; !ok {
		t.Fatalf("expected MCP tool mcp_echo to be present: %+v", tools)
	}
}

func TestAgentAdapter_CallTool_DelegatesToRuntimeManager(t *testing.T) {
	manager := NewDefaultManager(newStubMCPManager())
	adapter := NewAgentAdapter(manager)

	output, err := adapter.CallTool(context.Background(), "", "mcp_echo", map[string]interface{}{
		"message": "hello",
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	text, ok := output.(string)
	if !ok {
		t.Fatalf("expected string output, got %T", output)
	}
	if text != "echo:hello" {
		t.Fatalf("unexpected tool output: %q", text)
	}
}

func TestAgentAdapter_FindTool_PrefersRuntimeManagerCollisionResolution(t *testing.T) {
	manager := NewDefaultManager(newStubMCPManager())
	adapter := NewAgentAdapter(manager)

	info, err := adapter.FindTool("bash")
	if err != nil {
		t.Fatalf("FindTool failed: %v", err)
	}
	if info.Name != "bash" {
		t.Fatalf("unexpected tool info: %+v", info)
	}
	if info.MCPName != "stub" {
		t.Fatalf("expected MCP collision winner, got %+v", info)
	}
}

func TestAgentAdapter_FindTool_PrefersLocalToolkitOverToolkitMCP(t *testing.T) {
	manager := NewDefaultManager(newToolkitShadowMCPManager())
	adapter := NewAgentAdapter(manager)

	info, err := adapter.FindTool("ls")
	if err != nil {
		t.Fatalf("FindTool failed: %v", err)
	}
	if info.Name != "ls" {
		t.Fatalf("unexpected tool info: %+v", info)
	}
	if info.MCPName != "" {
		t.Fatalf("expected local toolkit tool to win over toolkit MCP, got %+v", info)
	}
}

func TestManagerExecute_PrefersLocalToolkitOverToolkitMCP(t *testing.T) {
	manager := NewDefaultManager(newToolkitShadowMCPManager())

	tmpDir := t.TempDir()
	output, err := manager.Execute(context.Background(), "ls", map[string]interface{}{
		"path":  tmpDir,
		"depth": 1,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(output, "目录:") {
		t.Fatalf("expected local ls output, got %q", output)
	}
}

func TestAgentAdapter_FindTool_PrefersLocalToolkitOverExternalFilesystemMCPForWorkspaceReadTools(t *testing.T) {
	manager := NewDefaultManager(newFilesystemShadowMCPManager())
	adapter := NewAgentAdapter(manager)

	info, err := adapter.FindTool("ls")
	if err != nil {
		t.Fatalf("FindTool failed: %v", err)
	}
	if info.Name != "ls" {
		t.Fatalf("unexpected tool info: %+v", info)
	}
	if info.MCPName != "" {
		t.Fatalf("expected local toolkit tool to win over external filesystem MCP, got %+v", info)
	}
}

func TestManagerExecute_PrefersLocalToolkitOverExternalFilesystemMCPForWorkspaceReadTools(t *testing.T) {
	manager := NewDefaultManager(newFilesystemShadowMCPManager())

	tmpDir := t.TempDir()
	output, err := manager.Execute(context.Background(), "ls", map[string]interface{}{
		"path":  tmpDir,
		"depth": 1,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(output, "目录:") {
		t.Fatalf("expected local ls output, got %q", output)
	}
}

type stubMCPManager struct{}

func newStubMCPManager() *stubMCPManager {
	return &stubMCPManager{}
}

func (m *stubMCPManager) LoadConfig(configPath string) error {
	return nil
}

func (m *stubMCPManager) Start(ctx context.Context) error {
	return nil
}

func (m *stubMCPManager) Stop() error {
	return nil
}

func (m *stubMCPManager) ListTools() []*registry.ToolInfo {
	return []*registry.ToolInfo{
		{
			MCPName: "stub",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "mcp_echo",
				Description: "Echo a message",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		{
			MCPName: "stub",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "bash",
				Description: "MCP override for bash",
				InputSchema: map[string]interface{}{
					"type": "object",
				},
			},
		},
	}
}

func (m *stubMCPManager) FindTool(name string) (*registry.ToolInfo, error) {
	for _, tool := range m.ListTools() {
		if tool.Tool != nil && tool.Tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *stubMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	switch toolName {
	case "mcp_echo":
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: "echo:" + args["message"].(string)},
			},
		}, nil
	case "bash":
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: "mcp-bash"},
			},
		}, nil
	default:
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}
}

func (m *stubMCPManager) SetMCPEnabled(name string, enabled bool) error {
	return nil
}

func (m *stubMCPManager) GetMCPStatus(name string) (*config.MCPStatus, error) {
	return &config.MCPStatus{Name: name, Enabled: true}, nil
}

func (m *stubMCPManager) ListMCPs() []*config.MCPStatus {
	return nil
}

func (m *stubMCPManager) ReloadConfig() error {
	return nil
}

func (m *stubMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (m *stubMCPManager) ReadResource(ctx context.Context, mcpName, uri string) (*protocol.ReadResourceResult, error) {
	return &protocol.ReadResourceResult{}, nil
}

type toolkitShadowMCPManager struct {
	stubMCPManager
}

func newToolkitShadowMCPManager() *toolkitShadowMCPManager {
	return &toolkitShadowMCPManager{}
}

func (m *toolkitShadowMCPManager) ListTools() []*registry.ToolInfo {
	return []*registry.ToolInfo{
		{
			MCPName: "toolkit",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "ls",
				Description: "shadow ls",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
	}
}

func (m *toolkitShadowMCPManager) FindTool(name string) (*registry.ToolInfo, error) {
	for _, tool := range m.ListTools() {
		if tool.Tool != nil && tool.Tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *toolkitShadowMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, fmt.Errorf("toolkit MCP should not be called for %s", toolName)
}

type filesystemShadowMCPManager struct {
	stubMCPManager
}

func newFilesystemShadowMCPManager() *filesystemShadowMCPManager {
	return &filesystemShadowMCPManager{}
}

func (m *filesystemShadowMCPManager) ListTools() []*registry.ToolInfo {
	return []*registry.ToolInfo{
		{
			MCPName: "filesystem",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "ls",
				Description: "external filesystem ls shadow",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
		{
			MCPName: "filesystem",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        "glob",
				Description: "external filesystem glob shadow",
				InputSchema: map[string]interface{}{"type": "object"},
			},
		},
	}
}

func (m *filesystemShadowMCPManager) FindTool(name string) (*registry.ToolInfo, error) {
	for _, tool := range m.ListTools() {
		if tool.Tool != nil && tool.Tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *filesystemShadowMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, fmt.Errorf("external filesystem MCP should not be called for %s", toolName)
}
