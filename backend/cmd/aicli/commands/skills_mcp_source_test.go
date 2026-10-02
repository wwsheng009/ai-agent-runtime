package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

const stubMCPToolName = "stub__mcp_only"

// stubProcessMCPManager is a process-level MCP manager exposing exactly one tool.
// It stands in for MCPManagerInstance so the skills MCP source fallback can be
// observed end to end.
type stubProcessMCPManager struct{}

func newStubProcessMCPManager() *stubProcessMCPManager { return &stubProcessMCPManager{} }

func (m *stubProcessMCPManager) LoadConfig(configPath string) error { return nil }
func (m *stubProcessMCPManager) Start(ctx context.Context) error    { return nil }
func (m *stubProcessMCPManager) Stop() error                        { return nil }

func (m *stubProcessMCPManager) ListTools() []*mcpregistry.ToolInfo {
	return []*mcpregistry.ToolInfo{
		{
			MCPName: "stub",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        stubMCPToolName,
				Description: "stub tool exposed only by the process-level MCP manager",
			},
		},
	}
}

func (m *stubProcessMCPManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, nil
}

func (m *stubProcessMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	for _, info := range m.ListTools() {
		if info.Tool != nil && info.Tool.Name == toolName {
			return info, nil
		}
	}
	return nil, nil
}

func (m *stubProcessMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, nil
}

func (m *stubProcessMCPManager) SetMCPEnabled(name string, enabled bool) error { return nil }

func (m *stubProcessMCPManager) GetMCPStatus(name string) (*mcpconfig.MCPStatus, error) {
	return nil, nil
}

func (m *stubProcessMCPManager) ListMCPs() []*mcpconfig.MCPStatus { return nil }
func (m *stubProcessMCPManager) ReloadConfig() error              { return nil }

// TestInitSkillFunctions_FallsBackToProcessMCPWhenToolManagerHasNoMCP guards the
// MCP source selection in initSkillFunctionsWithManager.
//
// The old condition was `toolManager != nil`, which treated "a manager exists" as
// "the manager carries MCP". It does not: hosts build the tools manager with
// mcp=nil whenever no MCP chain is attached (chat_setup passes mcpForTools, which
// is nil when local MCP is off and there is no session MCP). In that state
// NewAgentAdapter(toolManager) yields a dead MCP surface — AgentAdapter reads MCP
// exclusively through manager.mcp — so the MCPManagerInstance fallback below was
// unreachable and skills silently lost MCP tools.
func TestInitSkillFunctions_FallsBackToProcessMCPWhenToolManagerHasNoMCP(t *testing.T) {
	originalMCP := MCPManagerInstance
	t.Cleanup(func() { MCPManagerInstance = originalMCP })

	MCPManagerInstance = newStubProcessMCPManager()
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, nil)
	if toolManager.MCPAvailable() {
		t.Fatal("precondition: manager built with mcp=nil must not report MCP availability")
	}

	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "SKILL.md"), []byte(mcpFallbackProbeSkillMarkdown), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	session := &ChatSession{
		Model:            "test/model",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, toolManager, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	if binding.mcpRuntime == nil {
		t.Fatal("expected a non-nil MCP runtime on the binding")
	}

	// The fallback must surface the process-level manager's tool, proving the
	// MCPManagerInstance branch is actually reachable now.
	found := false
	for _, info := range binding.mcpRuntime.ListTools() {
		if info.Name == stubMCPToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected the process-level MCP tool in the skill MCP surface")
	}
}

// TestInitSkillFunctions_PrefersToolManagerMCPWhenAvailable pins the priority
// order: a tools manager that does carry MCP must keep winning over the
// process-level manager, so the fallback never shadows the richer session surface.
func TestInitSkillFunctions_PrefersToolManagerMCPWhenAvailable(t *testing.T) {
	originalMCP := MCPManagerInstance
	t.Cleanup(func() { MCPManagerInstance = originalMCP })

	MCPManagerInstance = newStubProcessMCPManager()
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(newStubSessionMCPManager(), nil)
	if !toolManager.MCPAvailable() {
		t.Fatal("precondition: manager built with an MCP runtime must report availability")
	}

	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "SKILL.md"), []byte(mcpFallbackProbeSkillMarkdown), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	session := &ChatSession{
		Model:            "test/model",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, toolManager, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	names := map[string]bool{}
	for _, info := range binding.mcpRuntime.ListTools() {
		names[info.Name] = true
	}
	if !names[stubSessionMCPToolName] {
		t.Fatal("expected the tool-manager (session) MCP tool in the skill MCP surface")
	}
	if names[stubMCPToolName] {
		t.Fatal("process-level MCP must not leak in when the tool manager already carries MCP")
	}
}

// TestManager_MCPAvailable covers the probe itself: nil receiver, no-MCP manager,
// and a manager with an MCP runtime all behave distinctly.
func TestManager_MCPAvailable(t *testing.T) {
	var nilManager *runtimetools.Manager
	if nilManager.MCPAvailable() {
		t.Fatal("nil manager must not report MCP availability")
	}

	noMCP := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, nil)
	if noMCP.MCPAvailable() {
		t.Fatal("manager built without an MCP runtime must report unavailable")
	}

	withMCP := runtimetools.NewDefaultManagerWithRuntimeConfig(newStubSessionMCPManager(), nil)
	if !withMCP.MCPAvailable() {
		t.Fatal("manager built with an MCP runtime must report available")
	}
}

const stubSessionMCPToolName = "stub__session_only"

// stubSessionMCPManager is the session-level MCP surface handed to the tools
// manager, standing in for chat_setup's merged mcpForTools.
type stubSessionMCPManager struct {
	stubProcessMCPManager
}

func newStubSessionMCPManager() *stubSessionMCPManager { return &stubSessionMCPManager{} }

func (m *stubSessionMCPManager) ListTools() []*mcpregistry.ToolInfo {
	return []*mcpregistry.ToolInfo{
		{
			MCPName: "session",
			Enabled: true,
			Tool: &protocol.Tool{
				Name:        stubSessionMCPToolName,
				Description: "stub tool exposed by the session tool manager",
			},
		},
	}
}

func (m *stubSessionMCPManager) FindTool(toolName string) (*mcpregistry.ToolInfo, error) {
	for _, info := range m.ListTools() {
		if info.Tool != nil && info.Tool.Name == toolName {
			return info, nil
		}
	}
	return nil, nil
}

const mcpFallbackProbeSkillMarkdown = `---
name: mcp-fallback-probe
description: Regression probe for the skills MCP source fallback.
---

# MCP fallback probe

Use the configured MCP tool when asked.
`
