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
