package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// ============================================================================
// B4 stdout/stderr 边界验证
//
// §8.5 契约：stderr 只含诊断（log/panic/错误提示），不含任何渲染字节；
// stdout 只有经 gateway 盖章的渲染输出或 CommandTextWriter 的协议输出。
// 本文件用 interactive session 触发已知错误，断言 stderr 与渲染输出零交集；
// 管道模式（非交互）消费 stdout 时不含诊断字节。
// ============================================================================

// newB4UnifiedSession 构造带 unified renderer 的 interactive 会话：
// 渲染字节写入 terminal buffer（stdout 代理），诊断走 os.Stderr。
func newB4UnifiedSession(t *testing.T) (*ChatSession, *chatInteractionCoordinator, *bytes.Buffer) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coord.SetSurface(surface)
	var terminal bytes.Buffer
	if !coord.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)
	terminal.Reset()
	return session, coord, &terminal
}

// assertStderrZeroRenderIntersection：断言 stderr 不含 ANSI 转义（渲染字节
// 签名）且不含已渲染到屏幕的错误文本。
func assertStderrZeroRenderIntersection(t *testing.T, stderr string, terminal *bytes.Buffer, markers ...string) {
	t.Helper()
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("stderr contains ANSI escape bytes (render signature): %q", stderr)
	}
	for _, marker := range markers {
		if strings.Contains(stderr, marker) {
			t.Fatalf("stderr contains rendered text %q: %q", marker, stderr)
		}
	}
	if terminal.Len() == 0 {
		t.Fatal("expected render bytes on stdout proxy (terminal buffer)")
	}
}

// TestB4InteractiveErrorStderrZeroIntersection：interactive session 触发
// 已知错误（未迁移命令 fence + 非法参数），渲染字节只在 stdout 代理，
// stderr 零交集。
func TestB4InteractiveErrorStderrZeroIntersection(t *testing.T) {
	session, coord, terminal := newB4UnifiedSession(t)

	// 已知错误 1：/agents panel 未迁移到统一渲染器（fence 错误）。
	// 已知错误 2：/retry 不接受参数（语义错误）。
	_, stderr := captureStdoutStderr(t, func() {
		for _, input := range []string{"/agents panel", "/retry extra-arg"} {
			if dispatchChatCommand(session, input, false) {
				t.Fatalf("%s unexpectedly requested chat exit", input)
			}
		}
	})
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	assertStderrZeroRenderIntersection(t, stderr, terminal,
		"尚未迁移到统一渲染命令通道",
		"/retry 不接受参数",
	)
}

// TestB4InteractiveFenceErrorRenderedToStdout：fence 错误确实渲染到了
// stdout 代理（终端 buffer），证明错误走渲染通道而非 stderr。
func TestB4InteractiveFenceErrorRenderedToStdout(t *testing.T) {
	session, coord, terminal := newB4UnifiedSession(t)

	_, stderr := captureStdoutStderr(t, func() {
		if dispatchChatCommand(session, "/agents panel", false) {
			t.Fatal("/agents panel unexpectedly requested chat exit")
		}
	})
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	if !strings.Contains(terminal.String(), "尚未迁移到统一渲染命令通道") {
		t.Fatalf("fence error not rendered to stdout proxy: %q", terminal.String())
	}
	if strings.Contains(stderr, "尚未迁移到统一渲染命令通道") {
		t.Fatalf("fence error leaked to stderr: %q", stderr)
	}
}

// TestB4PipeModeStdoutNoDiagnostics：非交互管道模式，协议输出走
// stdout（CommandTextWriter 或命令协议路径），诊断（含 ANSI）不得混入。
func TestB4PipeModeStdoutNoDiagnostics(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{
		NoInteractive: true,
		JSONOutput:    false,
	}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var protocol bytes.Buffer
	coord.SetWriter(&protocol)

	// 管道模式触发错误：/retry 仅用于交互式 Composer。
	stdout, stderr := captureStdoutStderr(t, func() {
		if dispatchChatCommand(session, "/retry", false) {
			t.Fatal("/retry unexpectedly requested chat exit")
		}
	})

	out := stdout + protocol.String()
	if !strings.Contains(out, "/retry 仅用于交互式 Composer") {
		t.Fatalf("protocol output missing error text: %q", out)
	}
	// stdout 协议输出不含诊断签名。
	if strings.Contains(out, "\x1b") {
		t.Fatalf("protocol stdout contains ANSI diagnostics: %q", out)
	}
	if strings.Contains(out, "Info:") || strings.Contains(out, "WARN") {
		t.Fatalf("protocol stdout contains diagnostic prefix: %q", out)
	}
	// 错误文本可以出现在 stderr（诊断提示），但 stderr 不得出现 ANSI。
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("stderr contains ANSI escape bytes: %q", stderr)
	}
}

// TestB4InteractiveNormalTurnNoStderrRenderLeak：正常交互一轮（本地
// supplement），渲染字节不泄漏到 stderr。
func TestB4InteractiveNormalTurnNoStderrRenderLeak(t *testing.T) {
	session, coord, terminal := newB4UnifiedSession(t)

	_, stderr := captureStdoutStderr(t, func() {
		session.Interaction.RenderLocalSupplement("本地提示行")
	})
	coord.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coord)

	if !strings.Contains(terminal.String(), "本地提示行") {
		t.Fatalf("local supplement not rendered: %q", terminal.String())
	}
	assertStderrZeroRenderIntersection(t, stderr, terminal, "本地提示行")
}
