package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestChatDebugDisplayShowsEndpointList(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	cfg := config.DefaultRuntimeConfig()
	cfg.Observe.Enabled = true
	cfg.Observe.RoutePrefix = "/api/runtime/observe/v1"
	host := &localChatRuntimeHost{RuntimeConfig: cfg}
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	for _, expected := range []string{
		"HTTP 调试端点:",
		"loopback  (aicli --pprof 本机调试服务器)",
		"Base:",
		"http://127.0.0.1:43210",
		"GET http://127.0.0.1:43210/debug/pprof/  [enabled]",
		"GET http://127.0.0.1:43210/debug/chat/status  [enabled]",
		"GET http://127.0.0.1:43210/debug/chat/screen  [enabled]",
		"GET http://127.0.0.1:43210/debug/endpoints  [enabled]",
		"web  (aicli 微型 Web 客户端 / 远程调用 API)",
		"GET http://127.0.0.1:43210/web/api/screen  [enabled]",
		"POST http://127.0.0.1:43210/web/api/invoke  [enabled]",
		"runtime-observe  (Runtime Observation Plane)",
		"http://127.0.0.1:8101/api/runtime/observe/v1",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/capabilities  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/snapshot  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/sessions/{session_id}  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/events  [enabled]",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatDebugEndpointListText(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	cfg := config.DefaultRuntimeConfig()
	cfg.Observe.Enabled = true
	cfg.Observe.RoutePrefix = "/api/runtime/observe/v1"
	host := &localChatRuntimeHost{RuntimeConfig: cfg}
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}

	// 通过 display provider 模拟"当前活动会话"
	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })

	text := BuildChatDebugEndpointsText()
	for _, expected := range []string{
		"loopback  (aicli --pprof 本机调试服务器)\n",
		"  Base: http://127.0.0.1:43210\n",
		"GET http://127.0.0.1:43210/debug/pprof/  [enabled]",
		"web  (aicli 微型 Web 客户端 / 远程调用 API)\n",
		"  Base: http://127.0.0.1:43210/web\n",
		"GET http://127.0.0.1:43210/web/api/screen  [enabled]",
		"GET http://127.0.0.1:43210/web/api/events  [enabled]",
		"POST http://127.0.0.1:43210/web/api/input  [enabled]",
		"POST http://127.0.0.1:43210/web/api/invoke  [enabled]",
		"GET http://127.0.0.1:43210/web/api/turn  [enabled]",
		"GET http://127.0.0.1:43210/web/api/sessions  [enabled]",
		"POST http://127.0.0.1:43210/web/api/sessions/resume  [enabled]",
		"GET http://127.0.0.1:43210/web/api/config  [enabled]",
		"GET http://127.0.0.1:43210/web/api/skills  [enabled]",
		"GET http://127.0.0.1:43210/web/api/analysis  [enabled]",
		"GET http://127.0.0.1:43210/web/api/cache  [enabled]",
		"  Base: http://127.0.0.1:8101/api/runtime/observe/v1\n",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/capabilities  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/snapshot  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/sessions/{session_id}  [enabled]",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/events  [enabled]",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected endpoints text to contain %q, got:\n%s", expected, text)
		}
	}
}

func TestChatDebugEndpointListObserveDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	cfg := config.DefaultRuntimeConfig() // Observe.Enabled 默认 false
	host := &localChatRuntimeHost{RuntimeConfig: cfg}
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}

	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })

	text := BuildChatDebugEndpointsText()
	if !strings.Contains(text, "GET /api/runtime/observe/v1/capabilities  [disabled]") {
		t.Fatalf("expected observe disabled marker in endpoints text, got:\n%s", text)
	}
	if !strings.Contains(text, "GET /api/runtime/observe/v1/events  [disabled]") {
		t.Fatalf("expected observe disabled marker for events in endpoints text, got:\n%s", text)
	}
	if strings.Contains(text, "GET /api/runtime/observe/v1/capabilities  [enabled]") {
		t.Fatalf("observe disabled but endpoints text shows [enabled], got:\n%s", text)
	}
}

func TestChatDebugEndpointListNoSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return nil })

	text := BuildChatDebugEndpointsText()
	if !strings.Contains(text, "Debug Endpoints: no active chat session") {
		t.Fatalf("expected no-session message in endpoints text, got:\n%s", text)
	}

	snap := BuildChatDebugEndpointsSnapshot()
	if snap.Available {
		t.Fatalf("expected available=false for no-session snapshot, got %+v", snap)
	}
	if len(snap.Endpoints) != 0 {
		t.Fatalf("expected empty endpoints for no-session snapshot, got %d", len(snap.Endpoints))
	}
}

// TestChatDebugEndpointListWebFamily 锁定 P1a：web 分组必须完整登记端点族
// （sessions/config/skills/analysis/cache/runtime/turn），并暴露写鉴权要求。
func TestChatDebugEndpointListWebFamily(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	session := &ChatSession{ProviderName: "test", Model: "test-model"}

	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })

	snap := BuildChatDebugEndpointsSnapshot()
	if !snap.Available {
		t.Fatal("snapshot must be available")
	}
	if snap.WriteAuthHeader != ChatWebAuthTokenHeader {
		t.Fatalf("write_auth_header = %q, want %q", snap.WriteAuthHeader, ChatWebAuthTokenHeader)
	}
	if !strings.Contains(snap.WriteAuthHint, ChatWebAuthTokenHeader) {
		t.Fatalf("write_auth_hint = %q", snap.WriteAuthHint)
	}

	wantPaths := []string{
		"/web/",
		"/web/api/screen", "/web/api/status", "/web/api/runtime",
		"/web/api/events", "/web/api/events/schema",
		"/web/api/input", "/web/api/invoke", "/web/api/turn",
		"/web/api/sessions", "/web/api/sessions/new", "/web/api/sessions/resume",
		"/web/api/sessions/rename", "/web/api/sessions/delete",
		"/web/api/config", "/web/api/config/providers",
		"/web/api/config/providers/delete", "/web/api/config/providers/enabled",
		"/web/api/config/providers/fetch-models", "/web/api/config/providers/probe-models",
		"/web/api/config/providers/auto-import", "/web/api/config/chat",
		"/web/api/skills", "/web/api/analysis", "/web/api/cache",
	}
	registered := map[string]bool{}
	for _, ep := range snap.Endpoints {
		if ep.Scheme == "web" {
			registered[ep.Path] = true
		}
	}
	for _, path := range wantPaths {
		if !registered[path] {
			t.Fatalf("web endpoint %q missing from inventory (have %v)", path, registered)
		}
	}
}

// TestChatDebugEndpointListNewEndpointNotes 锁定新增端点的描述文本：
// /debug/endpoints 与关于页端点清单共用同一 note，必须覆盖关键参数与语义，
// 避免清单只剩路径而丢失用法说明。
func TestChatDebugEndpointListNewEndpointNotes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	session := &ChatSession{ProviderName: "test", Model: "test-model"}
	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })

	snap := BuildChatDebugEndpointsSnapshot()
	notes := map[string]string{}
	for _, ep := range snap.Endpoints {
		if ep.Scheme == "web" {
			notes[ep.Path] = ep.Note
		}
	}
	want := map[string][]string{
		"/web/api/invoke": {"wait_only", "timeout_ms", "session_id", "client_request_id", "text/event-stream", "usage"},
		"/web/api/turn":   {"?id=", "current", "recent", "duration_ms", "usage"},
		"/web/api/input":  {"queued"},
	}
	for path, keywords := range want {
		note := notes[path]
		if strings.TrimSpace(note) == "" {
			t.Fatalf("endpoint %q has no note in inventory", path)
		}
		for _, kw := range keywords {
			if !strings.Contains(note, kw) {
				t.Fatalf("note of %q missing %q: %q", path, kw, note)
			}
		}
	}
}

// TestChatDebugEndpointListTokenSurfaces 锁定令牌的三个显示面边界：
//   - TUI（/debug display）直接显示令牌原文并指向专用端点，便于人工复制；
//   - HTTP 侧（/debug/endpoints 的 JSON 与 ?format=text）**不得**包含令牌原文，
//     避免清单被脚本转发/贴进 issue 时连带泄露；
//   - 清单里登记 GET /web/api/token，脚本据此发现读取入口。
func TestChatDebugEndpointListTokenSurfaces(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const token = "0123456789abcdef0123456789abcdef"
	setChatWebAuthTokenForTest(token)
	defer setChatWebAuthTokenForTest("")

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "http://127.0.0.1:43210/debug/pprof/" })

	session := &ChatSession{ProviderName: "test", Model: "test-model"}
	prevDisplay := chatDebugDisplaySessionProvider
	defer func() { chatDebugDisplaySessionProvider = prevDisplay }()
	RegisterChatDebugDisplayProvider(func() *ChatSession { return session })

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})
	for _, want := range []string{"Token:", token, ChatWebAPITokenPath} {
		if !strings.Contains(output, want) {
			t.Fatalf("/debug display missing %q, got:\n%s", want, output)
		}
	}

	raw, err := MarshalChatDebugEndpointsJSON()
	if err != nil {
		t.Fatalf("marshal endpoints json: %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("endpoints JSON must not contain the token:\n%s", raw)
	}
	text := BuildChatDebugEndpointsText()
	if strings.Contains(text, token) {
		t.Fatalf("endpoints text must not contain the token:\n%s", text)
	}
	if !strings.Contains(text, ChatWebAPITokenPath) {
		t.Fatalf("endpoints text must list %s:\n%s", ChatWebAPITokenPath, text)
	}
}
