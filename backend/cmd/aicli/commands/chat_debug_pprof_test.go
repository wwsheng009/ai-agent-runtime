package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestChatDebugDisplayShowsPprofEndpoint(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	prev := chatDebugPprofProvider
	defer func() { chatDebugPprofProvider = prev }()

	const endpoint = "http://127.0.0.1:43210/debug/pprof/"
	RegisterChatDebugPprofProvider(func() string { return endpoint })

	session := &ChatSession{ProviderName: "test", Model: "test-model"}
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/debug display", false); quit {
			t.Fatal("expected debug command not to exit")
		}
	})

	for _, expected := range []string{
		"pprof 诊断:",
		"Endpoint:",
		endpoint,
		`go tool pprof "` + endpoint + `heap?gc=1"`,
		`go tool pprof "` + endpoint + `allocs"`,
		`go tool pprof "` + endpoint + `profile?seconds=30"`,
		`go tool pprof "` + endpoint + `trace?seconds=5"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected /debug display output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestChatDebugDisplayPprofNotEnabled(t *testing.T) {
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

	if !strings.Contains(output, "pprof 诊断:") {
		t.Fatalf("expected pprof section heading in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "未启用") {
		t.Fatalf("expected pprof disabled status in /debug display, got:\n%s", output)
	}
	if !strings.Contains(output, "AICLI_PPROF") {
		t.Fatalf("expected enable hint in /debug display, got:\n%s", output)
	}
}
