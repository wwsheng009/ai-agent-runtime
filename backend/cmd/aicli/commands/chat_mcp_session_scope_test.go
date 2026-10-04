// 会话级 MCP 启停（/mcp enable|disable <name> --session）契约测试：
// 不落盘、撤销已注册函数、全局停用时的临时连接与会话结束回收。

package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// withSessionMCPConfigLookup 注入固定的分层配置，避免测试命中真实工作区文件。
func withSessionMCPConfigLookup(t *testing.T, cfg *mcpconfig.Config) {
	t.Helper()
	previous := sessionMCPConfigLookupFrom
	sessionMCPConfigLookupFrom = func(string) (*mcpconfig.LayeredResult, error) {
		return &mcpconfig.LayeredResult{Config: cfg}, nil
	}
	t.Cleanup(func() { sessionMCPConfigLookupFrom = previous })
}

// withoutProcessMCPManager 把进程级 manager 置空，保证测试走配置回退分支。
func withoutProcessMCPManager(t *testing.T) {
	t.Helper()
	previous := MCPManagerInstance
	MCPManagerInstance = nil
	t.Cleanup(func() { MCPManagerInstance = previous })
}

// isolateProcessMCPManager 快照并在用例结束时恢复整组进程级 MCP 全局。
//
// 只恢复 MCPManagerInstance 不够：initMCPManagerWithSelectionFrom 会把
// configPath/selectionKey/layerBase 一起写成「本次生效」的指纹。只回滚实例、
// 留下指纹，会让后续按指纹早退（mcp_integration.go 的 `configPath == mcpManagerConfigPath`
// 分支）拿到不一致的组合。任何会经 prepareChatMCPManager 建连的用例都应调用它，
// 否则进程全局会泄漏给同包后续用例（实例非 nil → skill 侧拿到纯 MCP 工具面 →
// 依赖 builtin 工具的 skill 被静默跳过）。
func isolateProcessMCPManager(t *testing.T) {
	t.Helper()
	prevInstance := MCPManagerInstance
	prevConfigPath := mcpManagerConfigPath
	prevSelectionKey := mcpManagerSelectionKey
	prevLayerBase := mcpManagerLayerBase
	t.Cleanup(func() {
		MCPManagerInstance = prevInstance
		mcpManagerConfigPath = prevConfigPath
		mcpManagerSelectionKey = prevSelectionKey
		mcpManagerLayerBase = prevLayerBase
	})
}

func newSessionScopeTestSession() *ChatSession {
	registry := functions.NewFunctionRegistry()
	return &ChatSession{
		FunctionCatalog:  newAICLIFunctionCatalog("openai", registry),
		FunctionRegistry: registry,
	}
}

// registerSessionTestMCPTool 以 runtime-tool 形态注册一个带 mcp_name 的工具。
func registerSessionTestMCPTool(t *testing.T, session *ChatSession, server, tool string) string {
	t.Helper()
	desc := runtimetools.ToolDescriptor{
		Name:        tool,
		Description: "test tool",
		Parameters:  map[string]interface{}{"type": "object"},
		Metadata:    map[string]interface{}{"mcp_name": server},
	}
	session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(nil, desc), desc)
	return tool
}

// stubSessionScopedMCPManager 是会话级临时连接的测试替身（只覆盖被调用的方法）。
type stubSessionScopedMCPManager struct {
	manager.Manager
	cfg     *mcpconfig.Config
	started bool
	stopped int
	tools   []*mcpregistry.ToolInfo
}

var (
	_ manager.ScopedManager = (*stubSessionScopedMCPManager)(nil)
	_ manager.AsyncManager  = (*stubSessionScopedMCPManager)(nil)
)

func (s *stubSessionScopedMCPManager) LoadConfigFromConfig(cfg *mcpconfig.Config) error {
	s.cfg = cfg
	return nil
}

func (s *stubSessionScopedMCPManager) StartAsync(context.Context) error {
	s.started = true
	return nil
}

func (s *stubSessionScopedMCPManager) WaitReady(context.Context) error { return nil }

func (s *stubSessionScopedMCPManager) Stop() error {
	s.stopped++
	return nil
}

func (s *stubSessionScopedMCPManager) ListTools() []*mcpregistry.ToolInfo {
	return s.tools
}

func TestChatMCPSessionToggleArgsParsing(t *testing.T) {
	name, scoped, errText := parseChatMCPToggleArgs([]string{"demo", "--session"}, "disable")
	if errText != "" || name != "demo" || !scoped {
		t.Fatalf("parse demo --session = %q/%v/%q", name, scoped, errText)
	}
	if _, _, errText := parseChatMCPToggleArgs([]string{"--session"}, "disable"); errText == "" {
		t.Fatal("missing name must produce usage error")
	}
	if _, _, errText := parseChatMCPToggleArgs([]string{"demo", "extra"}, "enable"); errText == "" {
		t.Fatal("extra positional args must be rejected")
	}
}

func TestChatMCPSessionDisableIsScopedAndPrunes(t *testing.T) {
	withoutProcessMCPManager(t)
	withSessionMCPConfigLookup(t, &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"local-fs": {Name: "local-fs", Enabled: true},
	}})

	session := newSessionScopeTestSession()
	registerSessionTestMCPTool(t, session, "local-fs", "read_file")

	// 默认路径（不带 --session）仍走持久化服务，不产生会话覆盖。
	service := newFakeChatMCPService()
	_ = chatMCPCommandTextWithSessionService(session, "/mcp disable local-fs", service, nil)
	if len(service.toggled) != 1 {
		t.Fatalf("persisted disable must call SetEnabled, toggled=%v", service.toggled)
	}
	if _, ok := sessionMCPOverrideLookup(session, "local-fs"); ok {
		t.Fatal("persisted disable must not create session override")
	}
	if _, exists := session.FunctionCatalog.Registry().Get("read_file"); !exists {
		t.Fatal("persisted disable must not prune session tool by itself")
	}

	// --session：不落盘、撤销工具、记录覆盖。
	mutated := 0
	text := chatMCPCommandTextWithSessionService(session, "/mcp disable local-fs --session", service, func() { mutated++ })
	if len(service.toggled) != 1 {
		t.Fatalf("--session must not persist, toggled=%v", service.toggled)
	}
	if !strings.Contains(text, "仅本会话") {
		t.Fatalf("session disable text = %q", text)
	}
	if !sessionMCPOverrideDisabled(session, "local-fs") {
		t.Fatal("session override must record disabled")
	}
	if _, exists := session.FunctionCatalog.Registry().Get("read_file"); exists {
		t.Fatal("session disable must prune registered tool (fail closed)")
	}
	if mutated != 1 {
		t.Fatalf("onMutate calls = %d, want 1", mutated)
	}

	// enable --session（全局启用）：清除覆盖并按全局面恢复登记。
	text = chatMCPCommandTextWithSessionService(session, "/mcp enable local-fs --session", service, nil)
	if !strings.Contains(text, "恢复启用") {
		t.Fatalf("session enable text = %q", text)
	}
	if _, ok := sessionMCPOverrideLookup(session, "local-fs"); ok {
		t.Fatal("session enable must clear the override for a globally enabled server")
	}
}

func TestChatMCPSessionSelectionFilterAndPrune(t *testing.T) {
	session := newSessionScopeTestSession()
	registerSessionTestMCPTool(t, session, "alpha", "alpha_tool")

	session.MCPSessionOverrides = map[string]bool{"alpha": false}
	selection := &aicliFunctionSelection{
		FinalFunctionNames: []string{"alpha_tool"},
		BuiltinFunctions:   []string{"alpha_tool"},
		Schemas:            []map[string]interface{}{{"name": "alpha_tool"}},
	}
	filtered := session.FunctionCatalog.filterSessionHiddenMCPFunctions(session, selection)
	if len(filtered.FinalFunctionNames) != 0 || len(filtered.Schemas) != 0 || len(filtered.BuiltinFunctions) != 0 {
		t.Fatalf("hidden MCP function must be filtered: %#v", filtered)
	}

	session.MCPSessionOverrides = map[string]bool{"alpha": true}
	if filtered := session.FunctionCatalog.filterSessionHiddenMCPFunctions(session, selection); len(filtered.FinalFunctionNames) != 1 {
		t.Fatalf("non-disabled override must not filter: %#v", filtered)
	}

	session.MCPSessionOverrides = map[string]bool{"alpha": false}
	removed := pruneSessionMCPFunctions(session, "alpha")
	if removed != 1 {
		t.Fatalf("prune removed = %d, want 1", removed)
	}
	if _, exists := session.FunctionCatalog.Registry().Get("alpha_tool"); exists {
		t.Fatal("pruned tool must be unregistered")
	}
}

func TestChatMCPSessionTempEnableConnectsAndRecycles(t *testing.T) {
	withoutProcessMCPManager(t)
	withSessionMCPConfigLookup(t, &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{
		"edge-tools": {Name: "edge-tools", Enabled: false, Disabled: true},
	}})

	stub := &stubSessionScopedMCPManager{tools: []*mcpregistry.ToolInfo{{
		MCPName: "edge-tools",
		Enabled: true,
		Tool: &protocol.Tool{
			Name:        "edge_tool",
			Description: "edge tool",
			InputSchema: map[string]interface{}{"type": "object"},
		},
	}}}
	previousFactory := sessionMCPTempManagerFactory
	sessionMCPTempManagerFactory = func() manager.Manager { return stub }
	t.Cleanup(func() { sessionMCPTempManagerFactory = previousFactory })

	session := newSessionScopeTestSession()
	service := newFakeChatMCPService()

	text := chatMCPCommandTextWithSessionService(session, "/mcp enable edge-tools --session", service, nil)
	if !strings.Contains(text, "临时启用") {
		t.Fatalf("temp enable text = %q", text)
	}
	if len(service.toggled) != 0 {
		t.Fatalf("temp enable must not persist, toggled=%v", service.toggled)
	}
	if stub.cfg == nil || !stub.started {
		t.Fatal("scoped manager must receive in-memory config and start")
	}
	server, ok := stub.cfg.MCPServers["edge-tools"]
	if !ok || !server.IsEnabled() {
		t.Fatalf("temp config must force enabled, got %#v", stub.cfg.MCPServers)
	}
	if session.mcpSessionTemp == nil || !session.mcpSessionTemp.hasServer("edge-tools") {
		t.Fatal("session must track the temp server")
	}
	if len(session.mcpSessionTemp.registered) == 0 {
		t.Fatal("temp tool must be registered into the session surface")
	}
	registeredName := ""
	for name := range session.mcpSessionTemp.registered {
		registeredName = name
	}
	if _, exists := session.FunctionCatalog.Registry().Get(registeredName); !exists {
		t.Fatalf("temp tool %q must be visible in the catalog", registeredName)
	}

	// 再次 enable 幂等。
	text = chatMCPCommandTextWithSessionService(session, "/mcp enable edge-tools --session", service, nil)
	if !strings.Contains(text, "已是本会话临时启用状态") {
		t.Fatalf("idempotent enable text = %q", text)
	}

	// disable --session：回收临时连接并撤销函数。
	text = chatMCPCommandTextWithSessionService(session, "/mcp disable edge-tools --session", service, nil)
	if !strings.Contains(text, "临时连接已回收") {
		t.Fatalf("temp disable text = %q", text)
	}
	if stub.stopped == 0 {
		t.Fatal("temp manager must be stopped on session disable")
	}
	if session.mcpSessionTemp != nil {
		t.Fatal("temp runtime must be released")
	}
	if _, exists := session.FunctionCatalog.Registry().Get(registeredName); exists {
		t.Fatalf("temp tool %q must be retracted", registeredName)
	}
}

func TestChatMCPListAnnotatesSessionState(t *testing.T) {
	withoutProcessMCPManager(t)
	session := newSessionScopeTestSession()
	session.MCPSessionOverrides = map[string]bool{"local-fs": false}

	service := newFakeChatMCPService()
	service.items = []mcpadmin.Item{{Config: mcpconfig.MCPConfig{Name: "local-fs", Type: "stdio", Enabled: true}}}

	text := chatMCPListTextWithSession(service, session)
	if !strings.Contains(text, "本会话: 已停用") {
		t.Fatalf("list must annotate session override, got:\n%s", text)
	}
}
