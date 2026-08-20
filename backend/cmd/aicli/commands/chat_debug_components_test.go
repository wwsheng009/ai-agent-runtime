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
		"调试入口与组件:",
		"Observe API:",
		"已启用",
		"route=/api/runtime/observe/v1",
		"GET /api/runtime/observe/v1/capabilities",
		"GET /api/runtime/observe/v1/snapshot",
		"GET /api/runtime/observe/v1/sessions/{session_id}",
		"GET /api/runtime/observe/v1/events",
		"[enabled]",
		"组件:",
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
		"调试入口与组件:",
		"Observe API:",
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

	if !strings.Contains(output, "调试入口与组件:") {
		t.Fatalf("expected 调试入口与组件 heading in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "Observe API:") {
		t.Fatalf("expected Observe API label in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "<未配置>") {
		t.Fatalf("expected <未配置> observe status without runtime host, got:\n%s", output)
	}
}
