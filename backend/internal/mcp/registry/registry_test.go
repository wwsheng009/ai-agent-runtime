package registry

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// mockClient 模拟 MCP 客户端
type mockClient struct {
	name string
}

func (m *mockClient) Connect(ctx context.Context) error {
	return nil
}

func (m *mockClient) Name() string {
	return m.name
}

func (m *mockClient) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	return nil, nil
}

func (m *mockClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, nil
}

func (m *mockClient) ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (m *mockClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	return nil, nil
}

func (m *mockClient) Close() error {
	return nil
}

func (m *mockClient) IsConnected() bool {
	return true
}

func TestNewRegistry(t *testing.T) {
	reg := NewRegistry()
	if reg == nil {
		t.Fatal("Expected non-nil registry")
	}

	tools := reg.ListTools()
	if tools == nil {
		t.Error("Expected nil tools list, not nil")
	}

	clients := reg.ListClients()
	if clients == nil {
		t.Error("Expected nil clients list, not nil")
	}
}

func TestRegisterClient(t *testing.T) {
	reg := NewRegistry()
	client := &mockClient{name: "test_client"}

	reg.RegisterClient("test_mcp", client)

	clients := reg.ListClients()
	if len(clients) != 1 {
		t.Errorf("Expected 1 client, got %d", len(clients))
	}

	if clients[0] != "test_mcp" {
		t.Errorf("Expected client name 'test_mcp', got %s", clients[0])
	}
}

func TestUnregisterClient(t *testing.T) {
	reg := NewRegistry()
	client := &mockClient{name: "test_client"}

	reg.RegisterClient("test_mcp", client)

	// 注销客户端
	reg.UnregisterClient("test_mcp")

	clients := reg.ListClients()
	if len(clients) != 0 {
		t.Errorf("Expected 0 clients, got %d", len(clients))
	}
}

func TestUnregisterClient_WithTools(t *testing.T) {
	reg := NewRegistry()
	client := &mockClient{name: "test_client"}

	reg.RegisterClient("test_mcp", client)

	// 注册工具
	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}
	reg.RegisterTool("test_mcp", tool, true)

	// 验证工具已注册
	tools := reg.ListTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	// 注销客户端
	reg.UnregisterClient("test_mcp")

	// 验证工具已被删除
	tools = reg.ListTools()
	if len(tools) != 0 {
		t.Errorf("Expected 0 tools after client unregistered, got %d", len(tools))
	}
}

func TestRegisterTool(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	reg.RegisterTool("test_mcp", tool, true)

	tools := reg.ListTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	if tools[0].Tool.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", tools[0].Tool.Name)
	}

	if tools[0].MCPName != "test_mcp" {
		t.Errorf("Expected MCP name 'test_mcp', got %s", tools[0].MCPName)
	}

	if !tools[0].Enabled {
		t.Error("Expected tool to be enabled")
	}
}

func TestRegisterTool_Disabled(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	reg.RegisterTool("test_mcp", tool, false)

	tools := reg.ListTools()
	if len(tools) != 0 {
		// ListTools 只返回启用的工具
		t.Errorf("Expected 0 enabled tools, got %d", len(tools))
	}
}

func TestUnregisterTool(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	reg.RegisterTool("test_mcp", tool, true)

	// 注销工具
	reg.UnregisterTool("test_mcp", "test_tool")

	tools := reg.ListTools()
	if len(tools) != 0 {
		t.Errorf("Expected 0 tools, got %d", len(tools))
	}
}

func TestListTools(t *testing.T) {
	reg := NewRegistry()

	// 注册多个工具
	tool1 := &protocol.Tool{
		Name:        "tool1",
		Description: "Tool 1",
		InputSchema: map[string]interface{}{},
	}
	tool2 := &protocol.Tool{
		Name:        "tool2",
		Description: "Tool 2",
		InputSchema: map[string]interface{}{},
	}
	tool3 := &protocol.Tool{
		Name:        "tool3",
		Description: "Tool 3",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("mcp1", tool1, true)
	reg.RegisterTool("mcp1", tool2, true)
	reg.RegisterTool("mcp2", tool3, false) // 禁用

	tools := reg.ListTools()
	if len(tools) != 2 {
		t.Errorf("Expected 2 enabled tools, got %d", len(tools))
	}
}

func TestListToolsByMCP(t *testing.T) {
	reg := NewRegistry()

	tool1 := &protocol.Tool{
		Name:        "tool1",
		Description: "Tool 1",
		InputSchema: map[string]interface{}{},
	}
	tool2 := &protocol.Tool{
		Name:        "tool2",
		Description: "Tool 2",
		InputSchema: map[string]interface{}{},
	}
	tool3 := &protocol.Tool{
		Name:        "tool3",
		Description: "Tool 3",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("mcp1", tool1, true)
	reg.RegisterTool("mcp1", tool2, true)
	reg.RegisterTool("mcp2", tool3, true)

	tools := reg.ListToolsByMCP("mcp1")
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools for mcp1, got %d", len(tools))
	}

	tools = reg.ListToolsByMCP("mcp2")
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool for mcp2, got %d", len(tools))
	}

	tools = reg.ListToolsByMCP("nonexistent")
	if len(tools) != 0 {
		t.Errorf("Expected 0 tools for nonexistent MCP, got %d", len(tools))
	}
}

func TestGetTool(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("test_mcp", tool, true)

	info, err := reg.GetTool("test_mcp", "test_tool")
	if err != nil {
		t.Fatalf("Failed to get tool: %v", err)
	}

	if info.Tool.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", info.Tool.Name)
	}

	if info.MCPName != "test_mcp" {
		t.Errorf("Expected MCP name 'test_mcp', got %s", info.MCPName)
	}
}

func TestGetTool_NotFound(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.GetTool("test_mcp", "nonexistent_tool")
	if err == nil {
		t.Error("Expected error for nonexistent tool, got nil")
	}
}

func TestEnableTool(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("test_mcp", tool, false)

	// 初始状态：工具禁用
	tools := reg.ListTools()
	if len(tools) != 0 {
		t.Errorf("Expected 0 enabled tools initially, got %d", len(tools))
	}

	// 启用工具
	err := reg.EnableTool("test_mcp", "test_tool")
	if err != nil {
		t.Fatalf("Failed to enable tool: %v", err)
	}

	// 验证工具已启用
	tools = reg.ListTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 enabled tool, got %d", len(tools))
	}

	if !reg.ToolEnabled("test_mcp", "test_tool") {
		t.Error("Expected tool to be enabled")
	}
}

func TestEnableTool_NotFound(t *testing.T) {
	reg := NewRegistry()

	err := reg.EnableTool("test_mcp", "nonexistent_tool")
	if err == nil {
		t.Error("Expected error for nonexistent tool, got nil")
	}
}

func TestDisableTool(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("test_mcp", tool, true)

	// 验证工具初始是启用状态
	if !reg.ToolEnabled("test_mcp", "test_tool") {
		t.Error("Expected tool to be enabled initially")
	}

	// 禁用工具
	err := reg.DisableTool("test_mcp", "test_tool")
	if err != nil {
		t.Fatalf("Failed to disable tool: %v", err)
	}

	// 验证工具已禁用
	tools := reg.ListTools()
	if len(tools) != 0 {
		t.Errorf("Expected 0 enabled tools, got %d", len(tools))
	}

	if reg.ToolEnabled("test_mcp", "test_tool") {
		t.Error("Expected tool to be disabled")
	}
}

func TestDisableTool_NotFound(t *testing.T) {
	reg := NewRegistry()

	err := reg.DisableTool("test_mcp", "nonexistent_tool")
	if err == nil {
		t.Error("Expected error for nonexistent tool, got nil")
	}
}

func TestToolEnabled(t *testing.T) {
	reg := NewRegistry()

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}

	// 工具不存在
	if reg.ToolEnabled("test_mcp", "nonexistent_tool") {
		t.Error("Expected nonexistent tool to not be enabled")
	}

	// 注册并启用工具
	reg.RegisterTool("test_mcp", tool, true)
	if !reg.ToolEnabled("test_mcp", "test_tool") {
		t.Error("Expected tool to be enabled")
	}

	// 禁用工具
	reg.DisableTool("test_mcp", "test_tool")
	if reg.ToolEnabled("test_mcp", "test_tool") {
		t.Error("Expected tool to be disabled")
	}
}

func TestGetClient(t *testing.T) {
	reg := NewRegistry()
	client := &mockClient{name: "test_client"}

	reg.RegisterClient("test_mcp", client)

	cli, err := reg.GetClient("test_mcp")
	if err != nil {
		t.Fatalf("Failed to get client: %v", err)
	}

	if cli == nil {
		t.Error("Expected non-nil client")
	}
}

func TestGetClient_NotFound(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.GetClient("nonexistent_mcp")
	if err == nil {
		t.Error("Expected error for nonexistent client, got nil")
	}
}

func TestListClients(t *testing.T) {
	reg := NewRegistry()

	clients := reg.ListClients()
	if len(clients) != 0 {
		t.Errorf("Expected 0 clients initially, got %d", len(clients))
	}

	// 注册多个客户端
	reg.RegisterClient("mcp1", &mockClient{name: "client1"})
	reg.RegisterClient("mcp2", &mockClient{name: "client2"})
	reg.RegisterClient("mcp3", &mockClient{name: "client3"})

	clients = reg.ListClients()
	if len(clients) != 3 {
		t.Errorf("Expected 3 clients, got %d", len(clients))
	}
}

func TestClear(t *testing.T) {
	reg := NewRegistry()

	// 注册客户端和工具
	reg.RegisterClient("test_mcp", &mockClient{name: "client"})

	tool := &protocol.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{},
	}
	reg.RegisterTool("test_mcp", tool, true)

	// 验证已注册
	if len(reg.ListClients()) != 1 {
		t.Error("Expected 1 client")
	}
	if len(reg.ListTools()) != 1 {
		t.Error("Expected 1 tool")
	}

	// 清空注册表
	reg.Clear()

	// 验证已清空
	if len(reg.ListClients()) != 0 {
		t.Error("Expected 0 clients after clear")
	}
	if len(reg.ListTools()) != 0 {
		t.Error("Expected 0 tools after clear")
	}
}

func TestMakeToolKey(t *testing.T) {
	reg := NewRegistry()

	key := reg.makeToolKey("mcp1", "tool1")
	if key != "mcp1_tool1" {
		t.Errorf("Expected key 'mcp1_tool1', got %s", key)
	}

	key = reg.makeToolKey("my_mcp", "myTool")
	if key != "my_mcp_myTool" {
		t.Errorf("Expected key 'my_mcp_myTool', got %s", key)
	}
}

func TestConcurrentAccess(t *testing.T) {
	reg := NewRegistry()

	// 并发注册工具
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			tool := &protocol.Tool{
				Name:        "tool",
				Description: "Test",
				InputSchema: map[string]interface{}{},
			}
			mcpName := "mcp"
			reg.RegisterTool(mcpName, tool, true)
			reg.ListTools()
			reg.ToolEnabled(mcpName, tool.Name)
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}

	// 验证没有panic或数据竞争
	tools := reg.ListTools()
	if tools == nil {
		t.Error("Expected tools list, got nil")
	}
}

func TestMultipleMCPsSameToolName(t *testing.T) {
	reg := NewRegistry()

	// 不同 MCP 使用相同工具名称
	tool1 := &protocol.Tool{
		Name:        "common_tool",
		Description: "Tool from MCP 1",
		InputSchema: map[string]interface{}{},
	}
	tool2 := &protocol.Tool{
		Name:        "common_tool",
		Description: "Tool from MCP 2",
		InputSchema: map[string]interface{}{},
	}

	reg.RegisterTool("mcp1", tool1, true)
	reg.RegisterTool("mcp2", tool2, true)

	tools := reg.ListTools()
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(tools))
	}

	// 验证两个工具来自不同的 MCP
	mcps := make(map[string]bool)
	for _, tool := range tools {
		mcps[tool.MCPName] = true
	}

	if !mcps["mcp1"] || !mcps["mcp2"] {
		t.Error("Expected tools from both mcp1 and mcp2")
	}
}
