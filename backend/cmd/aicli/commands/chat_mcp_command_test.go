// /mcp 子命令契约测试：子命令解析、请求映射、错误路径与刷新回调。

package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

type fakeChatMCPService struct {
	path    string
	items   []mcpadmin.Item
	configs map[string]*config.MCPConfig
	listErr error

	added   []mcpadmin.UpsertRequest
	removed []string
	toggled []string
	reloads int
}

func (f *fakeChatMCPService) ConfigPath() string { return f.path }

func (f *fakeChatMCPService) List(context.Context) ([]mcpadmin.Item, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.items, nil
}

func (f *fakeChatMCPService) Get(_ context.Context, name string) (*config.MCPConfig, error) {
	cfg, ok := f.configs[name]
	if !ok {
		return nil, fmt.Errorf("not found: %s", name)
	}
	return cfg, nil
}

func (f *fakeChatMCPService) Add(_ context.Context, req mcpadmin.UpsertRequest) (*config.MCPConfig, error) {
	f.added = append(f.added, req)
	cfg := &config.MCPConfig{Name: req.Name, Type: req.Type, URL: req.URL, Command: req.Command, Args: req.Args, Env: req.Env}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	return cfg, nil
}

func (f *fakeChatMCPService) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeChatMCPService) SetEnabled(_ context.Context, name string, enabled bool) (*config.MCPConfig, error) {
	f.toggled = append(f.toggled, fmt.Sprintf("%s=%t", name, enabled))
	return &config.MCPConfig{Name: name, Enabled: enabled}, nil
}

func (f *fakeChatMCPService) Reload(context.Context) error {
	f.reloads++
	return nil
}

func newFakeChatMCPService() *fakeChatMCPService {
	return &fakeChatMCPService{
		path:    `C:\Users\tester\.aicli\mcp.yaml`,
		configs: map[string]*config.MCPConfig{},
	}
}

func TestChatMCPListTextRendersEntries(t *testing.T) {
	service := newFakeChatMCPService()
	service.items = []mcpadmin.Item{
		{
			Config: config.MCPConfig{Name: "chrome-mcp", Type: "streamable", URL: "http://127.0.0.1:12306/mcp", Enabled: true},
			Status: &config.MCPStatus{Name: "chrome-mcp", Type: "streamable", Enabled: true, Connected: true, ToolCount: 27, TrustLevel: config.MCPTrustLevelUntrusted},
		},
		{
			Config: config.MCPConfig{Name: "local-fs", Type: "stdio", Command: "npx", Args: []string{"-y", "server-fs"}, Enabled: false},
			Status: &config.MCPStatus{Name: "local-fs", Type: "stdio", Enabled: false, Connected: false},
		},
	}

	text := chatMCPCommandTextWithService("/mcp list", service, nil)
	for _, want := range []string{
		"MCP Servers（2 个，已连接 1 个）",
		service.path,
		"● chrome-mcp [streamable] 已连接 · 27 tools · untrusted_remote",
		"http://127.0.0.1:12306/mcp",
		"○ local-fs [stdio] 已停用",
		"npx -y server-fs",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("list text missing %q:\n%s", want, text)
		}
	}
}

func TestChatMCPListTextEmptyAndError(t *testing.T) {
	service := newFakeChatMCPService()
	if text := chatMCPCommandTextWithService("/mcp", service, nil); !strings.Contains(text, "当前没有配置 MCP Server") {
		t.Fatalf("empty list text mismatch: %s", text)
	}
	service.listErr = fmt.Errorf("boom")
	if text := chatMCPCommandTextWithService("/mcp list", service, nil); !strings.Contains(text, "错误: 读取 MCP 列表失败: boom") {
		t.Fatalf("list error text mismatch: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp list", nil, nil); !strings.Contains(text, "MCP 管理服务不可用") {
		t.Fatalf("nil service text mismatch: %s", text)
	}
}

func TestChatMCPStatusText(t *testing.T) {
	service := newFakeChatMCPService()
	service.configs["chrome-mcp"] = &config.MCPConfig{
		Name:             "chrome-mcp",
		Type:             "streamable",
		URL:              "http://127.0.0.1:12306/mcp",
		Description:      "Chrome 浏览器控制",
		Enabled:          true,
		TrustLevel:       config.MCPTrustLevelTrustedRemote,
		MaxParallelCalls: 4,
		Env:              map[string]string{"HEADER_Authorization": "Bearer x"},
	}
	service.items = []mcpadmin.Item{{
		Config: *service.configs["chrome-mcp"],
		Status: &config.MCPStatus{Name: "chrome-mcp", Type: "streamable", Enabled: true, Connected: true, ToolCount: 27, LastError: ""},
	}}

	text := chatMCPCommandTextWithService("/mcp status chrome-mcp", service, nil)
	for _, want := range []string{"chrome-mcp", "类型: streamable", "信任级别: trusted_remote", "地址: http://127.0.0.1:12306/mcp", "描述: Chrome 浏览器控制", "并行上限: 4", "环境变量: 1 项", "运行状态: 已连接（27 tools）"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}

	if text := chatMCPCommandTextWithService("/mcp status ghost", service, nil); !strings.Contains(text, `错误: 读取 MCP "ghost" 失败`) {
		t.Fatalf("missing status error: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp status", service, nil); !strings.Contains(text, "用法: /mcp status <name>") {
		t.Fatalf("missing status usage: %s", text)
	}
}

func TestChatMCPAddURLTransport(t *testing.T) {
	service := newFakeChatMCPService()
	mutated := 0
	text := chatMCPCommandTextWithService(
		`/mcp add chrome-mcp http://127.0.0.1:12306/mcp --header "Authorization: Bearer x" --env API_KEY=1 --trust trusted_remote --description "浏览器"`,
		service,
		func() { mutated++ },
	)
	if mutated != 1 {
		t.Fatalf("expected onMutate once, got %d", mutated)
	}
	if len(service.added) != 1 {
		t.Fatalf("expected one add request, got %d", len(service.added))
	}
	req := service.added[0]
	if req.Name != "chrome-mcp" || req.Type != "streamable" || req.URL != "http://127.0.0.1:12306/mcp" {
		t.Fatalf("unexpected add request: %+v", req)
	}
	if req.Enabled == nil || !*req.Enabled {
		t.Fatalf("expected enabled=true, got %+v", req.Enabled)
	}
	if req.TrustLevel != "trusted_remote" {
		t.Fatalf("trust level mismatch: %q", req.TrustLevel)
	}
	if req.Description == nil || *req.Description != "浏览器" {
		t.Fatalf("description mismatch: %+v", req.Description)
	}
	if req.Headers["Authorization"] != "Bearer x" {
		t.Fatalf("headers mismatch: %+v", req.Headers)
	}
	if req.Env["API_KEY"] != "1" {
		t.Fatalf("env mismatch: %+v", req.Env)
	}
	if !strings.Contains(text, `已新增 MCP "chrome-mcp"`) || !strings.Contains(text, "配置: "+service.path) {
		t.Fatalf("add text mismatch: %s", text)
	}
}

func TestChatMCPAddStdioTransport(t *testing.T) {
	service := newFakeChatMCPService()
	chatMCPCommandTextWithService(`/mcp add local-fs --command npx --arg -y --arg server-fs --env TOKEN=abc --disabled`, service, nil)
	if len(service.added) != 1 {
		t.Fatalf("expected one add request, got %d", len(service.added))
	}
	req := service.added[0]
	if req.Type != "stdio" || req.Command != "npx" {
		t.Fatalf("unexpected stdio request: %+v", req)
	}
	if len(req.Args) != 2 || req.Args[0] != "-y" || req.Args[1] != "server-fs" {
		t.Fatalf("args mismatch: %+v", req.Args)
	}
	if req.Enabled == nil || *req.Enabled {
		t.Fatalf("expected enabled=false for --disabled, got %+v", req.Enabled)
	}
}

func TestChatMCPAddValidation(t *testing.T) {
	service := newFakeChatMCPService()
	cases := []struct {
		command string
		want    string
	}{
		{"/mcp add", "需要指定 MCP 名称"},
		{"/mcp add name", "stdio 传输需要 --command <cmd> 或 <command>"},
		{"/mcp add name --type sse", "URL 传输需要 <url> 或 --url"},
		{"/mcp add name --command", "--command 需要取值"},
		{"/mcp add name http://x --bogus", "未知参数 --bogus"},
	}
	for _, tc := range cases {
		text := chatMCPCommandTextWithService(tc.command, service, nil)
		if !strings.Contains(text, tc.want) {
			t.Fatalf("%s missing %q:\n%s", tc.command, tc.want, text)
		}
	}
	if len(service.added) != 0 {
		t.Fatalf("validation failures must not call Add, got %d calls", len(service.added))
	}
}

func TestChatMCPMutationsAndReload(t *testing.T) {
	service := newFakeChatMCPService()
	mutated := 0
	onMutate := func() { mutated++ }

	if text := chatMCPCommandTextWithService("/mcp enable chrome-mcp", service, onMutate); !strings.Contains(text, `已启用 MCP "chrome-mcp"`) {
		t.Fatalf("enable text mismatch: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp disable chrome-mcp", service, onMutate); !strings.Contains(text, `已停用 MCP "chrome-mcp"`) {
		t.Fatalf("disable text mismatch: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp remove chrome-mcp", service, onMutate); !strings.Contains(text, `已移除 MCP "chrome-mcp"`) {
		t.Fatalf("remove text mismatch: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp reload", service, onMutate); !strings.Contains(text, "已热重载 MCP 配置") {
		t.Fatalf("reload text mismatch: %s", text)
	}
	if len(service.toggled) != 2 || service.toggled[0] != "chrome-mcp=true" || service.toggled[1] != "chrome-mcp=false" {
		t.Fatalf("toggled mismatch: %+v", service.toggled)
	}
	if len(service.removed) != 1 || service.removed[0] != "chrome-mcp" {
		t.Fatalf("removed mismatch: %+v", service.removed)
	}
	if service.reloads != 1 {
		t.Fatalf("reloads=%d want 1", service.reloads)
	}
	if mutated != 4 {
		t.Fatalf("onMutate calls=%d want 4", mutated)
	}

	if text := chatMCPCommandTextWithService("/mcp enable", service, onMutate); !strings.Contains(text, "用法: /mcp enable <name>") {
		t.Fatalf("missing enable usage: %s", text)
	}
}

func TestChatMCPHelpAndUnknownSubcommand(t *testing.T) {
	service := newFakeChatMCPService()
	if text := chatMCPCommandTextWithService("/mcp help", service, nil); !strings.Contains(text, "/mcp status <name>") {
		t.Fatalf("help text mismatch: %s", text)
	}
	if text := chatMCPCommandTextWithService("/mcp bogus", service, nil); !strings.Contains(text, `未知子命令 "bogus"`) {
		t.Fatalf("unknown subcommand mismatch: %s", text)
	}
}

// /mcp help 不触碰配置文件，可安全走真实结构化分发，验证路由接线。
func TestStructuredMCPCommandRouteClaimsHelp(t *testing.T) {
	result, handled, err := tryExecuteStructuredChatCommand(&ChatSession{}, "/mcp help")
	if err != nil || !handled {
		t.Fatalf("structured match=(%t, %v), want handled", handled, err)
	}
	if result.Action != CommandContinue {
		t.Fatalf("action=%v want CommandContinue", result.Action)
	}
	if plain := ui.RenderDocumentPlain(result.Document()); !strings.Contains(plain, "/mcp status <name>") {
		t.Fatalf("structured /mcp help document missing usage:\n%s", plain)
	}
}
