//go:build !win7compat

// 本文件用 modelcontextprotocol/go-sdk 起真实 MCP server 验证 ACP 主机的
// 服务器下发路径。go-sdk 要求 go >= 1.23，不在 go.win7.mod 依赖图内，且
// Win7 兼容构建整体禁用 MCP（见 manager_win7compat.go），因此整文件排除。

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

func TestParseACPMCPMode(t *testing.T) {
	cases := map[string]acpMCPMode{
		"":       acpMCPModeMerge,
		"merge":  acpMCPModeMerge,
		" MERGE": acpMCPModeMerge,
		"local":  acpMCPModeLocal,
		"client": acpMCPModeClient,
		"off":    acpMCPModeOff,
	}
	for raw, want := range cases {
		got, err := parseACPMCPMode(raw)
		if err != nil {
			t.Fatalf("parseACPMCPMode(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("parseACPMCPMode(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := parseACPMCPMode("sometimes"); err == nil {
		t.Fatal("expected error for unknown mode")
	}

	// 零值必须收敛到 merge：chat/TUI/exec 与单元测试都依赖旧行为。
	var zero acpMCPMode
	if !zero.allowsLocal() || !zero.allowsClient() {
		t.Fatalf("zero mode must behave as merge, got local=%v client=%v", zero.allowsLocal(), zero.allowsClient())
	}
	if acpMCPModeOff.allowsLocal() || acpMCPModeOff.allowsClient() {
		t.Fatal("off must disable both sources")
	}
	if acpMCPModeClient.allowsLocal() || !acpMCPModeClient.allowsClient() {
		t.Fatal("client must allow only client-provided servers")
	}
	if !acpMCPModeLocal.allowsLocal() || acpMCPModeLocal.allowsClient() {
		t.Fatal("local must allow only the local config chain")
	}
}

func TestPlanACPSessionMCPGating(t *testing.T) {
	plan := &acpMCPClientPlan{Servers: []acp.McpServer{
		{Name: "fs", Type: acp.MCPTransportStdio, Command: "npx", Args: []string{"-y", "fs"}},
		{Name: "remote", Type: acp.MCPTransportHTTP, URL: "https://example.com/mcp"},
		{Name: "events", Type: acp.MCPTransportSSE, URL: "https://example.com/sse"},
	}}
	allCaps := acp.MCPCapabilities{HTTP: true, SSE: true}

	// merge + 已信任 + 全能力位：全部通过，且顺序稳定。
	planned, diagnostics := planACPSessionMCP(plan, acpMCPModeMerge, allCaps, true, "/workspace")
	if len(diagnostics) != 0 || len(planned) != 3 {
		t.Fatalf("planned=%d diagnostics=%+v", len(planned), diagnostics)
	}
	if planned[0].Name != "fs" || planned[1].Name != "remote" || planned[2].Name != "events" {
		t.Fatalf("planned names = %+v", planned)
	}

	// 模式门控：local 忽略全部客户端下发（旧行为）。
	planned, diagnostics = planACPSessionMCP(plan, acpMCPModeLocal, allCaps, true, "/workspace")
	if len(planned) != 0 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "mode=local") {
		t.Fatalf("local mode planned=%d diagnostics=%+v", len(planned), diagnostics)
	}

	// 工作区信任门控（D1）：未信任时不产生任何计划，且诊断列出被拒的 server。
	planned, diagnostics = planACPSessionMCP(plan, acpMCPModeMerge, allCaps, false, "/workspace")
	if len(planned) != 0 {
		t.Fatalf("untrusted workspace must plan nothing, got %+v", planned)
	}
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "untrusted workspace") ||
		!strings.Contains(diagnostics[0], "fs") || !strings.Contains(diagnostics[0], "remote") {
		t.Fatalf("untrusted diagnostics = %+v", diagnostics)
	}

	// 能力位门控（D7）：未通告的传输零连接尝试地跳过，stdio 不受影响。
	planned, diagnostics = planACPSessionMCP(plan, acpMCPModeMerge, acp.MCPCapabilities{}, true, "/workspace")
	if len(planned) != 1 || planned[0].Name != "fs" {
		t.Fatalf("planned=%+v, want only stdio", planned)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want http+sse skips", diagnostics)
	}
	for _, line := range diagnostics {
		if !strings.Contains(line, "not advertised") {
			t.Fatalf("unexpected diagnostic: %s", line)
		}
	}

	// 同名条目去重：后者保留独立身份，不覆盖前者的配置。
	dup := &acpMCPClientPlan{Servers: []acp.McpServer{
		{Name: "same", Type: acp.MCPTransportStdio, Command: "a"},
		{Name: "same", Type: acp.MCPTransportStdio, Command: "b"},
	}}
	planned, _ = planACPSessionMCP(dup, acpMCPModeMerge, allCaps, true, "/workspace")
	if len(planned) != 2 || planned[0].Name != "same" || planned[1].Name != "same-2" {
		t.Fatalf("dedupe planned = %+v", planned)
	}
	if planned[0].Config.Command != "a" || planned[1].Config.Command != "b" {
		t.Fatalf("dedupe lost config: %+v", planned)
	}
}

func TestACPMCPServerToConfigMapping(t *testing.T) {
	stdio, err := acpMCPServerToConfig(acp.McpServer{
		Name:    "fs",
		Command: "npx",
		Args:    []string{"-y", "fs"},
		Env:     []acp.MCPKeyValue{{Name: "TOKEN", Value: "v"}},
	}, "/workspace")
	if err != nil {
		t.Fatalf("stdio mapping: %v", err)
	}
	if stdio.Type != "stdio" || stdio.Command != "npx" || stdio.WorkingDir != "/workspace" {
		t.Fatalf("stdio config = %+v", stdio)
	}
	if !stdio.Enabled || stdio.TrustLevel != config.MCPTrustLevelLocal {
		t.Fatalf("stdio trust/enabled = %+v", stdio)
	}
	if stdio.Env["TOKEN"] != "v" {
		t.Fatalf("stdio env = %+v", stdio.Env)
	}

	http, err := acpMCPServerToConfig(acp.McpServer{
		Name:    "remote",
		Type:    acp.MCPTransportHTTP,
		URL:     "https://example.com/mcp",
		Headers: []acp.MCPKeyValue{{Name: "Authorization", Value: "Bearer x"}},
	}, "/workspace")
	if err != nil {
		t.Fatalf("http mapping: %v", err)
	}
	if http.Type != "streamable" || http.URL != "https://example.com/mcp" {
		t.Fatalf("http config = %+v", http)
	}
	if http.Headers["Authorization"] != "Bearer x" || http.Env != nil {
		t.Fatalf("http headers/env = %+v / %+v", http.Headers, http.Env)
	}
	if http.TrustLevel != config.MCPTrustLevelUntrusted {
		t.Fatalf("remote trust = %q", http.TrustLevel)
	}

	sse, err := acpMCPServerToConfig(acp.McpServer{Name: "events", Type: acp.MCPTransportSSE, URL: "https://example.com/sse"}, "")
	if err != nil || sse.Type != "sse" {
		t.Fatalf("sse config = %+v err=%v", sse, err)
	}

	if _, err := acpMCPServerToConfig(acp.McpServer{Name: "bad", Command: " "}, ""); err == nil {
		t.Fatal("expected error for empty command")
	}
}

// TestZedLoadSampleShapeIsRecognized 用 Zed 实机 session/load 报文（2026-09-20 抓取）
// 走完整识别链路：DecodeMCPServers -> newACPMCPClientPlan -> planACPSessionMCP。
//
// 报文形状的三个易错点：
//  1. 条目无 "type" 字段 -> 必须按隐式 stdio 识别；
//  2. "env": [] 空数组 -> 必须被当作「无环境变量」而不是非法条目；
//  3. Windows 绝对路径 command + 正斜杠 args -> 必须原样保留（不做路径归一化）。
func TestZedLoadSampleShapeIsRecognized(t *testing.T) {
	raw := json.RawMessage(`[{
		"name": "mcp-server-context7",
		"command": "C:\\Program Files\\nodejs\\node.exe",
		"args": ["C:/Users/vince/AppData/Local/Zed/extensions/work/mcp-server-context7/node_modules/@upstash/context7-mcp/dist/index.js"],
		"env": []
	}]`)

	plan := newACPMCPClientPlan(raw)
	if plan == nil {
		t.Fatal("Zed sample must decode into a plan, got nil")
	}
	if len(plan.Issues) != 0 {
		t.Fatalf("Zed sample must not be skipped, issues = %+v", plan.Issues)
	}
	if len(plan.Servers) != 1 {
		t.Fatalf("servers = %+v", plan.Servers)
	}
	server := plan.Servers[0]
	if server.TransportKind() != acp.MCPTransportStdio {
		t.Fatalf(`missing "type" must decode as stdio, got %q`, server.TransportKind())
	}
	if server.Command != `C:\Program Files\nodejs\node.exe` {
		t.Fatalf("command = %q", server.Command)
	}
	if len(server.Args) != 1 || !strings.HasSuffix(server.Args[0], "context7-mcp/dist/index.js") {
		t.Fatalf("args = %+v", server.Args)
	}
	if len(server.Env) != 0 {
		t.Fatalf("env=[] must decode to an empty list, got %+v", server.Env)
	}

	// 同一份报文必须通过 stdio 门控（不需要任何能力位）并映射成可直接建连的配置。
	cwd := `E:\projects\ai\ai-agent-runtime`
	planned, diagnostics := planACPSessionMCP(plan, acpMCPModeMerge, acp.MCPCapabilities{}, true, cwd)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(planned) != 1 {
		t.Fatalf("planned = %+v", planned)
	}
	cfg := planned[0].Config
	if cfg.Name != "mcp-server-context7" || cfg.Type != "stdio" || !cfg.Enabled {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Command != `C:\Program Files\nodejs\node.exe` || len(cfg.Args) != 1 {
		t.Fatalf("config command/args = %+v", cfg)
	}
	if cfg.Env != nil {
		t.Fatalf("env=[] must map to a nil env map, got %+v", cfg.Env)
	}
	if cfg.WorkingDir != cwd {
		t.Fatalf("working dir = %q, want %q", cfg.WorkingDir, cwd)
	}
	if cfg.TrustLevel != config.MCPTrustLevelLocal {
		t.Fatalf("trust level = %q", cfg.TrustLevel)
	}
}

func TestKeyValueMapLastWinsAndRedaction(t *testing.T) {
	mapped := keyValueMap([]acp.MCPKeyValue{
		{Name: "TOKEN", Value: "first"},
		{Name: " TOKEN ", Value: "second"},
		{Name: " ", Value: "ignored"},
	})
	if len(mapped) != 1 || mapped["TOKEN"] != "second" {
		t.Fatalf("keyValueMap = %+v, want last-wins TOKEN=second", mapped)
	}

	redacted := redactMCPKeyNames([]acp.MCPKeyValue{
		{Name: "B", Value: "secret-b"},
		{Name: "A", Value: "secret-a"},
	})
	if redacted != "A,B" {
		t.Fatalf("redactMCPKeyNames = %q, want A,B", redacted)
	}
	if strings.Contains(redacted, "secret") {
		t.Fatalf("redaction leaked a value: %q", redacted)
	}
}

func TestACPPlanContextRoundTrip(t *testing.T) {
	plan := &acpMCPClientPlan{Servers: []acp.McpServer{{Name: "fs", Command: "npx"}}}
	ctx := withACPMCPClientPlan(context.Background(), plan)
	if got := acpMCPClientPlanFromContext(ctx); got != plan {
		t.Fatalf("plan round trip = %+v, want %+v", got, plan)
	}
	if got := acpMCPClientPlanFromContext(context.Background()); got != nil {
		t.Fatalf("empty ctx must yield nil plan, got %+v", got)
	}
	// nil 计划不得污染 ctx（避免把「没有下发」误判成「下发为空」）。
	if got := acpMCPClientPlanFromContext(withACPMCPClientPlan(ctx, nil)); got != plan {
		t.Fatalf("nil plan must not clear the existing plan, got %+v", got)
	}
}

func TestNewACPMCPClientPlanDecode(t *testing.T) {
	raw := json.RawMessage(`[{"name":"fs","command":"npx"},{"name":"broken"}]`)
	plan := newACPMCPClientPlan(raw)
	if plan == nil || len(plan.Servers) != 1 || len(plan.Issues) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := newACPMCPClientPlan(nil); got != nil {
		t.Fatalf("empty payload must yield nil plan, got %+v", got)
	}
	if got := newACPMCPClientPlan(json.RawMessage("[]")); got != nil {
		t.Fatalf("empty array must yield nil plan, got %+v", got)
	}
}

// stubMCPManager 提供固定的工具清单，用于验证增量登记契约（D5/R1）。
type stubMCPManager struct {
	tools     []*registry.ToolInfo
	stopCalls int
}

func (s *stubMCPManager) LoadConfig(string) error          { return nil }
func (s *stubMCPManager) Start(context.Context) error      { return nil }
func (s *stubMCPManager) Stop() error                      { s.stopCalls++; return nil }
func (s *stubMCPManager) ListTools() []*registry.ToolInfo  { return s.tools }
func (s *stubMCPManager) ListMCPs() []*config.MCPStatus    { return nil }
func (s *stubMCPManager) ReloadConfig() error              { return nil }
func (s *stubMCPManager) SetMCPEnabled(string, bool) error { return nil }
func (s *stubMCPManager) ListResources(context.Context, string, *string) (*protocol.ListResourcesResult, error) {
	return nil, nil
}
func (s *stubMCPManager) GetMCPStatus(string) (*config.MCPStatus, error) { return nil, nil }
func (s *stubMCPManager) FindTool(name string) (*registry.ToolInfo, error) {
	return nil, fmt.Errorf("tool %q not found", name)
}
func (s *stubMCPManager) CallTool(context.Context, string, string, map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, nil
}

func TestACPSessionMCPRefreshToolsIsIncremental(t *testing.T) {
	stub := &stubMCPManager{}
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(stub, nil)
	catalog := newAICLIFunctionCatalog("openai", nil)

	// 与生产装配一致：先把「装配时刻已存在的工具」登记为基线，MCP 工具在
	// 连接完成之后才出现，因此只能由 turn 边界的增量刷新登记。
	baseline := registeredMCPToolNames(toolManager.ListTools())
	stub.tools = []*registry.ToolInfo{{
		MCPName: "client-server",
		Enabled: true,
		Tool:    &protocol.Tool{Name: "remote_lookup", Description: "lookup", InputSchema: map[string]interface{}{"type": "object"}},
	}}

	runtime := &acpSessionMCP{
		sessionID:   "test",
		manager:     stub,
		toolManager: toolManager,
		catalog:     catalog,
		registered:  baseline,
	}

	if added := runtime.refreshTools(); added != 1 {
		t.Fatalf("first refresh added %d tools, want 1", added)
	}
	// 幂等：同一批工具重复 refresh 不产生重复登记。
	if added := runtime.refreshTools(); added != 0 {
		t.Fatalf("second refresh added %d tools, want 0", added)
	}
	if !catalogHasTool(t, catalog, "remote_lookup") {
		t.Fatalf("catalog missing remote_lookup: %+v", catalog.Stats())
	}

	// 迟到的工具在下一个 turn 边界被增量登记（不依赖 session/new）。
	stub.tools = append(stub.tools, &registry.ToolInfo{
		MCPName: "client-server",
		Enabled: true,
		Tool:    &protocol.Tool{Name: "late_tool", Description: "late", InputSchema: map[string]interface{}{"type": "object"}},
	})
	if added := runtime.refreshTools(); added != 1 {
		t.Fatalf("late refresh added %d tools, want 1", added)
	}
	if !catalogHasTool(t, catalog, "late_tool") {
		t.Fatal("catalog missing late_tool after refresh")
	}

	// 关闭后不再登记，且 close 幂等。
	runtime.close()
	runtime.close()
	if added := runtime.refreshTools(); added != 0 {
		t.Fatalf("closed runtime registered %d tools, want 0", added)
	}
}

// TestACPSessionMCPSurfaceRefreshesLocalChainTools 锁定「只有本地配置链」的 ACP
// 会话同样在 turn 边界补登记迟到的 MCP 工具，且 close() 不回收进程级 manager。
func TestACPSessionMCPSurfaceRefreshesLocalChainTools(t *testing.T) {
	stub := &stubMCPManager{}
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(stub, nil)
	catalog := newAICLIFunctionCatalog("openai", nil)

	refresher := newACPSessionMCPSurface("test", catalog, stub)
	if refresher == nil {
		t.Fatal("surface refresher must be created for a non-nil local manager")
	}
	if newACPSessionMCPSurface("test", catalog, nil) != nil {
		t.Fatal("nil local manager must yield no refresher")
	}

	// 装配时刻本地配置链还没连上（StartAsync），工具面为空。
	refresher.toolManager = toolManager
	refresher.registered = registeredMCPToolNames(toolManager.ListTools())
	if _, exists := refresher.registered["local_echo"]; exists {
		t.Fatal("baseline must not contain the MCP tool before the local server connects")
	}

	// 本地 server 连上之后工具才出现：只能在 prompt 边界补登记。
	stub.tools = []*registry.ToolInfo{{
		MCPName: "local-e2e",
		Enabled: true,
		Tool:    &protocol.Tool{Name: "local_echo", Description: "echo", InputSchema: map[string]interface{}{"type": "object"}},
	}}
	refresher.prepareForPrompt(context.Background())
	if !catalogHasTool(t, catalog, "local_echo") {
		t.Fatalf("catalog missing late local MCP tool: %+v", catalog.Stats())
	}

	// 会话关闭不得回收进程级 manager。
	refresher.close()
	if stub.stopCalls != 0 {
		t.Fatalf("close() must not stop the process-level manager, Stop called %d time(s)", stub.stopCalls)
	}
}

func catalogHasTool(t *testing.T, catalog *aicliFunctionCatalog, name string) bool {
	t.Helper()
	if catalog == nil {
		return false
	}
	for _, schema := range catalog.BuiltinSchemas() {
		if schemaLookupKey(schema) == strings.ToLower(strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// catalogSchemaNames 返回目录中全部内建函数名（仅用于诊断失败原因）。
func catalogSchemaNames(catalog *aicliFunctionCatalog) []string {
	if catalog == nil {
		return nil
	}
	names := make([]string, 0)
	for _, schema := range catalog.BuiltinSchemas() {
		if got := schemaLookupKey(schema); got != "" {
			names = append(names, got)
		}
	}
	sort.Strings(names)
	return names
}

// TestACPSessionMCPStdioEndToEnd 用测试二进制自身充当 stdio MCP server，验证
// 客户端下发 → 会话级 manager → 工具登记 → 会话回收的完整链路。
func TestACPSessionMCPStdioEndToEnd(t *testing.T) {
	if os.Getenv("AICLI_MCP_HELPER") == "1" {
		t.Skip("helper process")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	plan := &acpMCPClientPlan{Servers: []acp.McpServer{{
		Name:    "helper",
		Type:    acp.MCPTransportStdio,
		Command: exe,
		Args:    []string{"-test.run=TestMCPHelperServerProcess", "-test.v=false"},
		Env:     []acp.MCPKeyValue{{Name: "AICLI_MCP_HELPER", Value: "1"}},
	}}}

	catalog := newAICLIFunctionCatalog("openai", nil)
	session := &ChatSession{
		FunctionCatalog: catalog,
		FolderTrust: foldertrust.Resolution{
			WorkspaceKey:   "test-workspace",
			FeatureEnabled: true,
			Trusted:        true,
			ProjectRoot:    t.TempDir(),
		},
	}

	runtime := buildACPSessionMCP(context.Background(), "test-session", session, acpMCPModeClient, plan, acp.MCPCapabilities{})
	if runtime == nil {
		t.Fatal("expected a session MCP runtime for a trusted stdio server")
	}
	defer runtime.close()
	if got := runtime.summary(); len(got) != 1 || got[0] != "helper" {
		t.Fatalf("summary = %+v", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime.waitReadyOnce(ctx, 20*time.Second)

	tools := runtime.manager.ListTools()
	if !containsMCPTool(tools, "echo_tool") {
		t.Fatalf("session manager tools = %+v, want echo_tool", toolNames(tools))
	}

	runtime.toolManager = runtimetools.NewDefaultManagerWithRuntimeConfig(runtime.manager, nil)
	if added := runtime.refreshTools(); added == 0 {
		t.Fatal("refreshTools registered nothing for a connected client server")
	}
	if !catalogHasTool(t, catalog, "echo_tool") {
		t.Fatalf("catalog missing echo_tool: %+v names=%v", catalog.Stats(), catalogSchemaNames(catalog))
	}

	// 工具调用必须落在会话级 manager 上（而不是进程级全局实例）。
	result, err := runtime.manager.CallTool(ctx, "helper", "echo_tool", map[string]interface{}{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result == nil || len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "echo:hi") {
		t.Fatalf("CallTool result = %+v", result)
	}

	// 回收后子进程连接必须断开。
	runtime.close()
	if tools := runtime.manager.ListTools(); len(tools) != 0 {
		t.Fatalf("tools still present after close: %+v", toolNames(tools))
	}
}

func TestBuildACPSessionMCPRefusesUntrustedWorkspace(t *testing.T) {
	plan := &acpMCPClientPlan{Servers: []acp.McpServer{{Name: "helper", Command: "definitely-not-a-real-binary"}}}
	session := &ChatSession{
		FunctionCatalog: newAICLIFunctionCatalog("openai", nil),
		FolderTrust: foldertrust.Resolution{
			WorkspaceKey:   "test-workspace",
			FeatureEnabled: true,
			Trusted:        false,
			ProjectRoot:    t.TempDir(),
		},
	}
	if runtime := buildACPSessionMCP(context.Background(), "test-session", session, acpMCPModeMerge, plan, acp.MCPCapabilities{}); runtime != nil {
		t.Fatalf("untrusted workspace must not attach a session MCP runtime, got %+v", runtime.summary())
	}
}

func containsMCPTool(tools []*registry.ToolInfo, name string) bool {
	for _, info := range tools {
		if info != nil && info.Tool != nil && info.Tool.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []*registry.ToolInfo) []string {
	names := make([]string, 0, len(tools))
	for _, info := range tools {
		if info != nil && info.Tool != nil {
			names = append(names, info.Tool.Name)
		}
	}
	return names
}

// TestMCPHelperServerProcess 在 AICLI_MCP_HELPER=1 时把测试二进制变成 stdio
// MCP server（供 TestACPSessionMCPStdioEndToEnd 拉起）。
func TestMCPHelperServerProcess(t *testing.T) {
	if os.Getenv("AICLI_MCP_HELPER") != "1" {
		t.Skip("helper process only")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "aicli-test-mcp", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo_tool", Description: "echo text"},
		func(_ context.Context, _ *mcp.CallToolRequest, input struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + input.Text}}}, nil, nil
		})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	// 测试框架会向 stdout 打印 "PASS"，那会污染 MCP 协议流：直接退出。
	os.Exit(0)
}
