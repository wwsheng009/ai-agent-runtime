package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestChatDebugDisplayShowsObserveEntrypointsAndComponents(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

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
		"运行时组件:",
		"Runtime Core:",
		"Observe Service:",
		"ready",
		"Observe Collector:",
		"Observe Redactor:",
		"key_ref=runtime-observe-fingerprint-v1",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatDebugDisplayObserveDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	cfg := config.DefaultRuntimeConfig() // Observe.Enabled 默认 false
	host := &localChatRuntimeHost{RuntimeConfig: cfg}
	session := &ChatSession{ProviderName: "test", Model: "test-model", LocalRuntimeHost: host}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	for _, expected := range []string{
		"运行时组件:",
		"Observe Service:",
		"未启用",
		"[disabled]",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "[enabled]") {
		t.Fatalf("observe disabled but /debug display shows [enabled], got:\n%s", output)
	}
}

func TestChatDebugDisplayShowsObserveActualBaseURL(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	cfg := config.DefaultRuntimeConfig()
	cfg.Observe.Enabled = true
	cfg.Observe.RoutePrefix = "/api/runtime/observe/v1"
	host := &localChatRuntimeHost{RuntimeConfig: cfg}
	session := &ChatSession{
		ProviderName:     "test",
		Model:            "test-model",
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIRuntimeServerChatExecutor("http://127.0.0.1:8101"),
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	for _, expected := range []string{
		"HTTP 调试端点:",
		"runtime-observe  (Runtime Observation Plane)",
		"Base:",
		"http://127.0.0.1:8101/api/runtime/observe/v1",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/capabilities",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/snapshot",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/sessions/{session_id}",
		"GET http://127.0.0.1:8101/api/runtime/observe/v1/events",
		"[enabled]",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatDebugDisplayWithoutRuntimeHost(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()
	RegisterChatDebugPprofProvider(func() string { return "" })

	session := &ChatSession{ProviderName: "test", Model: "test-model"}
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	if !strings.Contains(output, "运行时组件:") {
		t.Fatalf("expected 运行时组件 heading in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "Runtime Core:") {
		t.Fatalf("expected Runtime Core label in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "<none>") {
		t.Fatalf("expected <none> without runtime host, got:\n%s", output)
	}
}
