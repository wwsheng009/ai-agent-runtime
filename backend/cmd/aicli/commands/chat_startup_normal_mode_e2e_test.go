package commands

// e2e：复现 win11 正常模式（unified renderer）下
// "aicli chat 启动后 prompt 输入区消失 / 长时间无响应"。
//
// 与既有 driveTTYLiveLoop 测试的区别：显式走 EnableUnifiedRenderer
// （TerminalSessionPresenter / Scene 渲染），覆盖 unified 模式下
// prompt 渲染与输入可达性——这是用户 win11 报告问题的路径。

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

// TestE2E_ChatStartup_NormalMode_ReachesReadyAndRendersPrompt
// 正常模式（unified renderer）下：驱动真实主循环，注入 "hi" 与 "/exit"，
// 断言：预算内 Ready、prompt 输入区渲染到字节流、输入真实到达 executor。
// 若 unified 启动路径死锁/无限等待，waitReady 预算会暴露超时并 dump 全部
// goroutine 栈定位卡点。
func TestE2E_ChatStartup_NormalMode_ReachesReadyAndRendersPrompt(t *testing.T) {
	executor := &fakeChatExecutor{output: "unified e2e reply"}
	session := &ChatSession{
		Provider:     testChatProvider(),
		cancelCtx:    newTestChatContext(),
		ChatExecutor: executor,
		Logger:       NewChatLogger("codex_ee", "codex", "gpt-5.4", false, "https://example.com"),
		Formatter:    formatter.NewMarkdownFormatter(false),
		InputBox:     ui.NewInputBox(nil),
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.session = session

	const width, height = 80, 24
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	session.Interaction.SetSurface(surface)
	session.Interaction.SetWriter(os.Stdout)

	var raw string
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw = driveTTYLiveLoop(t, session, []ttyLiveScriptStep{
			{wait: 300 * time.Millisecond, line: "hi\n"},
			{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
		}, 30*time.Second)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("unified 启动/输入路径超时（60s）——疑似死锁/无限等待；goroutine dump:\n%s", buf[:n])
	}

	if !executor.called {
		screen := vt.NewScreen(width, height)
		screen.Feed(raw)
		t.Logf("--- raw bytes (%d) ---\n%q", len(raw), raw)
		t.Logf("--- rendered screen ---\n%s", screen.Dump())
		if session.Interaction != nil {
			t.Logf("interaction ready=%v", session.Interaction.IsReady())
		}
		t.Fatalf("executor 未被调用（输入未到达主循环输入路径）")
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(raw)
	lines := screen.Lines(0, height)
	t.Logf("--- unified rendered screen (%d bytes) ---\n%s", len(raw), screen.Dump())

	// prompt 输入区哨兵：输入回显或提示符「>」出现在屏幕上。
	foundPrompt := false
	foundReply := false
	for _, l := range lines {
		if strings.Contains(l, ">") || strings.Contains(l, "hi") {
			foundPrompt = true
		}
		if strings.Contains(l, "unified e2e reply") {
			foundReply = true
		}
	}
	if !foundPrompt {
		t.Errorf("屏幕未发现 prompt 输入区（无 > 或输入回显）; lines=%q", lines)
	}
	if !foundReply {
		t.Errorf("屏幕未发现 assistant 回复; lines=%q", lines)
	}
	assertNoAdjacentDuplicateLines(t, lines)
}

// TestE2E_ChatStartup_NormalMode_BuildSessionWithinBudget
// 正常模式 buildChatSession 全链路（交互 opts、surface/keyHandler/unified
// 挂接）必须在预算内完成；超时则 dump goroutine 栈，定位“启动 10 分钟无
// 响应”的初始化卡点。
func TestE2E_ChatStartup_NormalMode_BuildSessionWithinBudget(t *testing.T) {
	opts := &chatCommandOptions{}

	runtimeState := &chatRuntimeState{
		providerName:    "codex_ee",
		provider:        testChatProvider(),
		adapter:         &adapter.CodexAdapter{},
		modelName:       "gpt-5.4",
		reasoningEffort: "medium",
		shouldStream:    false,
		baseURL:         "https://example.com/v1/responses",
		retryCfg:        defaultRetryConfig(),
		requestTimeout:  30 * time.Second,
	}

	type result struct {
		session *ChatSession
		cleanup func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		s, c, err := buildChatSession(&agentconfig.Config{}, opts,
			nil, &chatPersistenceState{sessionUserID: "tester", resolvedSessionDir: t.TempDir()}, runtimeState)
		done <- result{session: s, cleanup: c, err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("buildChatSession: %v", r.err)
		}
		if r.cleanup != nil {
			defer r.cleanup()
		}
		if r.session == nil {
			t.Fatal("buildChatSession returned nil session")
		}
	case <-time.After(30 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("buildChatSession（正常模式）30s 未返回——疑似启动初始化卡死；goroutine dump:\n%s", buf[:n])
	}
}
