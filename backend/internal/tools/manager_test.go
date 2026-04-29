package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type toolkitErrorToolStub struct {
	name    string
	content string
	err     error
}

type toolkitSuccessToolStub struct {
	name       string
	content    string
	data       []byte
	mimeType   string
	outputKind string
	metadata   map[string]interface{}
}

func (s toolkitErrorToolStub) Name() string { return s.name }

func (s toolkitErrorToolStub) Description() string { return "toolkit error stub" }

func (s toolkitErrorToolStub) Version() string { return "test" }

func (s toolkitErrorToolStub) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}

func (s toolkitErrorToolStub) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return &toolkit.ToolResult{
		Success: false,
		Content: s.content,
		Error:   s.err,
	}, nil
}

func (s toolkitErrorToolStub) CanDirectCall() bool { return true }

func (s toolkitSuccessToolStub) Name() string { return s.name }

func (s toolkitSuccessToolStub) Description() string { return "toolkit success stub" }

func (s toolkitSuccessToolStub) Version() string { return "test" }

func (s toolkitSuccessToolStub) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}

func (s toolkitSuccessToolStub) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: s.outputKind,
		Content:    s.content,
		Data:       s.data,
		MIMEType:   s.mimeType,
		Metadata:   s.metadata,
	}, nil
}

func (s toolkitSuccessToolStub) CanDirectCall() bool { return true }

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

func TestNewDefaultManagerWithRuntimeConfig_ApplyPatchHonorsSandbox(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sandbox.Enabled = true
	cfg.Sandbox.ReadOnlyPaths = []string{tmpDir}

	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + filepath.Join(tmpDir, "blocked.txt"),
		"+sandbox",
		"*** End Patch",
	}, "\n")
	_, err := manager.Execute(context.Background(), "apply_patch", map[string]interface{}{
		"patch": patch,
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

func TestNewDefaultManagerWithRuntimeConfig_AllBuiltinToolkitToolsExposeOutputKind(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = tmpDir

	manager := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	argsByTool := map[string]map[string]interface{}{
		"bash":                  {"command": ""},
		"execute_shell_command": {"command": ""},
		"apply_patch":           {"patch": ""},
		"view":                  {},
		"edit":                  {},
		"write":                 {},
		"glob":                  {},
		"grep":                  {},
		"ls":                    {"path": tmpDir, "depth": 1},
		"download":              {},
		"fetch":                 {},
		"multiedit":             {},
		"todos":                 {},
		"sourcegraph":           {},
		"web_search":            {},
	}
	toolNames := []string{
		"bash",
		"execute_shell_command",
		"apply_patch",
		"view",
		"edit",
		"write",
		"glob",
		"grep",
		"ls",
		"download",
		"fetch",
		"multiedit",
		"todos",
		"sourcegraph",
		"web_search",
	}

	for _, name := range toolNames {
		t.Run(name, func(t *testing.T) {
			_, metadata, execErr := manager.ExecuteWithMeta(context.Background(), name, argsByTool[name])
			if metadata == nil {
				t.Fatalf("expected metadata for %s, got nil (err=%v)", name, execErr)
			}
			if got := metadata[toolresult.MetadataKey]; got != toolresult.KindText {
				t.Fatalf("expected %s=%q for %s, got %#v (err=%v)", toolresult.MetadataKey, toolresult.KindText, name, got, execErr)
			}
			if got := metadata[toolresult.SourceKey]; got != toolresult.SourceToolkit {
				t.Fatalf("expected %s=%q for %s, got %#v (err=%v)", toolresult.SourceKey, toolresult.SourceToolkit, name, got, execErr)
			}
		})
	}
}

func TestExecuteWithMeta_ListMCPResources_ExposesExplicitTextOutputKind(t *testing.T) {
	manager := NewDefaultManager(newMetaResourcesMCPManager())

	output, metadata, err := manager.ExecuteWithMeta(context.Background(), "list_mcp_resources", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if !strings.Contains(output, `"servers"`) {
		t.Fatalf("expected JSON output, got %q", output)
	}
	if metadata == nil {
		t.Fatal("expected metadata, got nil")
	}
	if got := metadata[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
	if got := metadata[toolresult.SourceKey]; got != toolresult.SourceMeta {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceMeta, got)
	}
	if got := metadata["output_size"]; got == nil {
		t.Fatal("expected output_size metadata")
	}
}

func TestExecuteWithMeta_MCPTool_ExposesExplicitSource(t *testing.T) {
	manager := NewDefaultManager(newStubMCPManager())

	output, metadata, err := manager.ExecuteWithMeta(context.Background(), "mcp_echo", map[string]interface{}{
		"message": "hello",
	})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != "echo:hello" {
		t.Fatalf("unexpected output: %q", output)
	}
	if metadata == nil {
		t.Fatal("expected metadata, got nil")
	}
	if got := metadata[toolresult.SourceKey]; got != toolresult.SourceMCP {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceMCP, got)
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

func TestManagerListTools_ReturnsSortedNames(t *testing.T) {
	manager := NewDefaultManager(newStubMCPManager())

	tools := manager.ListTools()
	if len(tools) == 0 {
		t.Fatal("expected manager to expose tools")
	}

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("expected sorted tool names, got %v", names)
	}
}

func TestNewDefaultManagerWithRuntimeConfig_RegistersOpenAIImageGenerateToolWhenProviderSupportsImagesGenerationsAPI(t *testing.T) {
	t.Cleanup(func() {
		_, _ = agentconfig.InitGlobalConfig("")
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := strings.TrimSpace(`
providers:
  items:
    openai_image:
      enabled: true
      type: openai
      base_url: https://api.openai.com
      api_key: test-key
      default_model: gpt-image-2
      supported_models:
        - gpt-image-2
      model_capabilities:
        gpt-image-2:
          native_tools:
            images_generations_api: true
`)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if _, err := agentconfig.InitGlobalConfig(configPath); err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}

	manager := NewDefaultManagerWithRuntimeConfig(nil, runtimecfg.DefaultRuntimeConfig())
	if _, ok := manager.toolkit.Get(toolnames.OpenAIImageGenerateToolName); !ok {
		t.Fatalf("expected %s tool to be registered", toolnames.OpenAIImageGenerateToolName)
	}
}

func TestNewDefaultManagerWithRuntimeConfig_SkipsOpenAIImageGenerateToolWithoutImagesGenerationsAPI(t *testing.T) {
	t.Cleanup(func() {
		_, _ = agentconfig.InitGlobalConfig("")
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := strings.TrimSpace(`
providers:
  items:
    openai_image:
      enabled: true
      type: openai
      base_url: https://api.openai.com
      api_key: test-key
      default_model: gpt-image-2
      supported_models:
        - gpt-image-2
      model_capabilities:
        gpt-image-2:
          native_tools:
            image_generation: true
`)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	if _, err := agentconfig.InitGlobalConfig(configPath); err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}

	manager := NewDefaultManagerWithRuntimeConfig(nil, runtimecfg.DefaultRuntimeConfig())
	if _, ok := manager.toolkit.Get(toolnames.OpenAIImageGenerateToolName); ok {
		t.Fatalf("expected %s tool to be skipped", toolnames.OpenAIImageGenerateToolName)
	}
}

func TestManager_ResolveToolSource_CanonicalizesLegacyImageGenerateAlias(t *testing.T) {
	registry := toolkit.NewRegistry()
	if err := registry.Register(toolkitSuccessToolStub{
		name:       toolnames.OpenAIImageGenerateToolName,
		content:    "ok",
		outputKind: toolresult.KindText,
	}); err != nil {
		t.Fatalf("register stub tool: %v", err)
	}

	manager := &Manager{toolkit: registry}
	if got := manager.resolveToolSource(toolnames.LegacyImageGenerateToolName); got != toolresult.SourceToolkit {
		t.Fatalf("expected legacy alias to resolve to toolkit source, got %q", got)
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

func TestManagerExecute_PreservesToolkitOutputOnError(t *testing.T) {
	manager := &Manager{toolkit: toolkit.NewRegistry()}
	if err := manager.toolkit.Register(toolkitErrorToolStub{
		name:    "toolkit_error",
		content: "fatal: not a git repository (or any of the parent directories): .git",
		err:     fmt.Errorf("exit status 128"),
	}); err != nil {
		t.Fatalf("register stub tool: %v", err)
	}

	output, err := manager.Execute(context.Background(), "toolkit_error", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected toolkit tool to fail")
	}
	if !strings.Contains(output, "fatal: not a git repository") {
		t.Fatalf("expected stderr output to be preserved, got %q", output)
	}
	if !strings.Contains(err.Error(), "exit status 128") {
		t.Fatalf("expected exit status in error, got %v", err)
	}
}

func TestManagerExecute_DoesNotWrapEmptySuccessfulTextResultAsJSON(t *testing.T) {
	manager := &Manager{toolkit: toolkit.NewRegistry()}
	if err := manager.toolkit.Register(toolkitSuccessToolStub{
		name:       "toolkit_empty_success",
		content:    "",
		outputKind: "text",
		metadata: map[string]interface{}{
			"output_size": 0,
		},
	}); err != nil {
		t.Fatalf("register stub tool: %v", err)
	}

	output, err := manager.Execute(context.Background(), "toolkit_empty_success", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if output != "" {
		t.Fatalf("expected empty string output, got %q", output)
	}
}

func TestAgentAdapter_CallToolWithMeta_PreservesOutputKind(t *testing.T) {
	manager := &Manager{toolkit: toolkit.NewRegistry()}
	if err := manager.toolkit.Register(toolkitSuccessToolStub{
		name:       "toolkit_text",
		content:    "hello",
		outputKind: "text",
	}); err != nil {
		t.Fatalf("register stub tool: %v", err)
	}

	adapter := NewAgentAdapter(manager)
	output, metadata, err := adapter.CallToolWithMeta(context.Background(), "", "toolkit_text", map[string]interface{}{})
	if err != nil {
		t.Fatalf("CallToolWithMeta failed: %v", err)
	}
	text, ok := output.(string)
	if !ok {
		t.Fatalf("expected string output, got %T", output)
	}
	if text != "hello" {
		t.Fatalf("unexpected tool output: %q", text)
	}
	if metadata["output_kind"] != "text" {
		t.Fatalf("expected output_kind=text, got %#v", metadata["output_kind"])
	}
	if metadata[toolresult.SourceKey] != toolresult.SourceToolkit {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceToolkit, metadata[toolresult.SourceKey])
	}
}

func TestFormatMCPResult_PreservesErrorText(t *testing.T) {
	output, _, err := formatMCPResult(&protocol.CallToolResult{
		IsError: true,
		Content: []protocol.Content{
			{Type: "text", Text: "fatal: command rejected"},
		},
	})
	if err == nil {
		t.Fatal("expected MCP error")
	}
	if output != "fatal: command rejected" {
		t.Fatalf("expected error text to be preserved, got %q", output)
	}
	if err.Error() != "fatal: command rejected" {
		t.Fatalf("unexpected MCP error: %v", err)
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

type metaResourcesMCPManager struct {
	stubMCPManager
}

func newMetaResourcesMCPManager() *metaResourcesMCPManager {
	return &metaResourcesMCPManager{}
}

func (m *metaResourcesMCPManager) ListMCPs() []*config.MCPStatus {
	return []*config.MCPStatus{
		{Name: "docs", Enabled: true},
	}
}

func (m *metaResourcesMCPManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	if mcpName != "docs" {
		return nil, fmt.Errorf("unexpected mcp: %s", mcpName)
	}
	return &protocol.ListResourcesResult{
		Resources: []*protocol.Resource{
			{
				URI:         "file://docs/guide.md",
				Name:        "guide",
				Description: "Project guide",
				MIMEType:    "text/markdown",
			},
		},
	}, nil
}
