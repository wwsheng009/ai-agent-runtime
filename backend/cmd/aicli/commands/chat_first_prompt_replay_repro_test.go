package commands

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestFreshSessionFirstPromptIsNotReplayedByLazyBridgeStart 复现 2026-11-02 现场：
// 全新会话（无 canonical 历史）的首条 prompt 已经由 RenderSubmittedUserInput
// 写入事件日志后，能力面才异步就绪；actor 首次 ensureChatRuntimeEventBridge →
// start() 重放事件日志。重放 reset 了 Scene 身份，当前 prompt 作为唯一记录被
// 再次铸造进原生 scrollback，用户看到首条 prompt 重复；而 AppState/Web 快照
// 仍只有一条，所以 /debug/chat/screen 看起来正常。
//
// 修复契约：事件日志重放不依赖 runtime host，必须在 bridge 首次 start（也就是
// 首条 prompt 注入之前）消费 replayOnce；host 就绪后的 start 只能订阅总线，
// 不得再重放本轮已经 live 注入过的 prompt。
func TestFreshSessionFirstPromptIsNotReplayedByLazyBridgeStart(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	const (
		sessionID     = "sess-fresh-first-prompt-replay"
		width, height = 96, 24
		question      = "FIRST-PROMPT-IN-FRESH-SESSION"
	)

	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	session := &ChatSession{
		Provider:       config.Provider{Protocol: "codex"},
		cancelCtx:      context.Background(),
		ChatExecutor:   &fakeChatExecutor{output: "answer"},
		Formatter:      formatter.NewMarkdownFormatter(false),
		RuntimeSession: &runtimechat.Session{ID: sessionID},
		Stream:         true,
	}
	// 首个 prompt 之前的 bridge：能力面尚未挂载（LocalRuntimeHost == nil），
	// 与生产启动首帧后的窗口一致。
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath
	session.RuntimeEventBridge = bridge
	ensureChatRuntimeEventBridge(session) // start() 在 host 就绪前不订阅
	if _, _, replayed, _ := bridge.eventLogStats(); replayed != 0 {
		t.Fatalf("precondition: replay happened before the first prompt: replayed=%d", replayed)
	}

	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}

	// 首条 prompt：live 注入 → 写事件日志 → 交给 actor 执行。
	coordinator.RenderSubmittedUserInput(question)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	// 让 live user cell 真正完成原生 scrollback 交付后再触发 host 就绪：
	// 真机重复发生在已交付行被重放重新铸造；若不等交付，测试会因 ledger
	// 尚未 ack 而假绿。
	if delivered := awaitHistoryCommitDelivered(t, coordinator); delivered == 0 {
		t.Fatal("live first prompt was never delivered to the terminal history ledger")
	}
	if count := strings.Count(terminal.String(), question); count != 1 {
		t.Fatalf("live first prompt rendered %d blocks before actor start, want 1\n%s",
			count, tailString(terminal.String(), 2000))
	}

	// 能力面（runtime host / EventBus）异步就绪：actor 首次 ensure 触发 start()。
	session.LocalRuntimeHost = &localChatRuntimeHost{EventBus: runtimeevents.NewBus()}
	ensureChatRuntimeEventBridge(session)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	if count := strings.Count(terminal.String(), question); count != 1 {
		t.Fatalf("lazy bridge start replayed the live first prompt: terminal count=%d, want exactly 1\n%s",
			count, tailString(terminal.String(), 3000))
	}
	if _, _, replayed, _ := bridge.eventLogStats(); replayed != 0 {
		t.Fatalf("lazy bridge start replayed the event log after the live first prompt: replayed=%d, want 0", replayed)
	}
	state := coordinator.uiActor.AppState()
	assertTranscriptSourceCount(t, state.Transcript.Cells, question, 1)
}
