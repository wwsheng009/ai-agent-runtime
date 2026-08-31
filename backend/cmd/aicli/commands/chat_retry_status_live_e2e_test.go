package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// L3 进程内 e2e：llm.retry 事件 → chatRuntimeEventBridge → UI actor →
// 动态状态行渲染 → 计时器启动并随真实时间推进。
//
// 回归场景（方案 B）：旧实现把 "retrying step=1 ..." 当非运行态字符串处理，
// 时钟不启动，状态行永远渲染 "(0s • esc to interrupt)"。本测试断言：
//  1. llm.retry 到达后时钟立即启动（dynamicStatusStarted 非零）；
//  2. 真实时间流逝 >=1s 后手动驱动一次动态状态 tick（等价 UI actor 的
//     Timer 回调，chat_ui_actor.go 的 FrameKeyDynamicStatus 分支），
//     渲染文本必须推进到 "(1s • esc to interrupt)" 而非卡在 0s。
func TestLLMRetryEventE2E_StartsClockAndAdvancesElapsed(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	var history bytes.Buffer
	interaction.SetWriter(&history)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})

	// 注入 llm.retry（真实运行中由 runtime server 事件流推送）。
	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.retry",
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "step": 1, "attempt": 2, "max_attempts": 3,
			"retry_reason": "rate_limit", "retry_delay_ms": 500,
		},
	})
	interaction.waitUIActorIdle()

	// 1) 重试是过程状态：不得泄漏到持久 timeline/transcript。
	if got := history.String(); strings.Contains(got, "retry") || strings.Contains(got, "llm") {
		t.Fatalf("retry leaked into durable history: %q", got)
	}

	// 2) 时钟必须已启动（bug 回归点：旧实现此处 started 为零 → 永远 0s）。
	interaction.mu.Lock()
	started := interaction.dynamicStatusStarted
	seq := interaction.dynamicStatusTimerSeq
	model := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if started.IsZero() {
		t.Fatal("llm.retry event must start the dynamic status clock")
	}
	if model == nil {
		t.Fatal("llm.retry event must render the dynamic status model")
	}
	initial := style.StatusLineDocument(*model, 160).PlainText()
	if !strings.Contains(initial, "Retrying step=1 attempt=2/3") || !strings.Contains(initial, "reason=rate_limit") {
		t.Fatalf("unexpected initial retry status: %q", initial)
	}
	if !strings.Contains(initial, "(0s • esc to interrupt)") {
		t.Fatalf("initial retry status must start at 0s, got: %q", initial)
	}

	// 3) 真实时间推进：elapsed 必须 >= 1s（不再冻结在 0s）。
	time.Sleep(1100 * time.Millisecond)
	elapsed := interaction.dynamicStatusElapsedLocked(time.Now())
	if elapsed < time.Second {
		t.Fatalf("retry elapsed did not advance after sleeping 1.1s: %v", elapsed)
	}

	// 4) 驱动一次 tick（测试模式下自动 tick 禁用，等价 UI actor Timer 回调
	//    chat_ui_actor.go: FrameKeyDynamicStatus → refreshDynamicStatusTick），
	//    渲染文本必须推进到 1s。
	interaction.refreshDynamicStatusTick(seq)
	interaction.mu.Lock()
	advanced := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if advanced == nil {
		t.Fatal("tick must keep the dynamic model rendered")
	}
	afterTick := style.StatusLineDocument(*advanced, 160).PlainText()
	if strings.Contains(afterTick, "(0s • esc to interrupt)") {
		t.Fatalf("retry timer stuck at 0s after real time passed: %q", afterTick)
	}
	if !strings.Contains(afterTick, "(1s • esc to interrupt)") &&
		!strings.Contains(afterTick, "(2s • esc to interrupt)") {
		t.Fatalf("retry timer must advance to 1s+, got: %q", afterTick)
	}
	if !strings.Contains(afterTick, "Retrying step=1 attempt=2/3") {
		t.Fatalf("tick must keep retry detail: %q", afterTick)
	}
}

// L3 进程内 e2e 补充：一个 turn 内多次 llm.retry 事件（attempt 递增）必须
// 复用同一个时钟（elapsed 连续累计），而不是每次重置为 0s。
func TestLLMRetryEventE2E_RepeatedRetriesKeepClockRunning(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	interaction.SetSurface(surface)
	session.Interaction = interaction

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	postRetry := func(attempt int) {
		t.Helper()
		bridge.handleEvent(runtimeevents.Event{
			Type:      "llm.retry",
			SessionID: "lead-session",
			TraceID:   "trace-1",
			Payload: map[string]interface{}{
				"turn_id": "turn-1", "step": 1, "attempt": attempt, "max_attempts": 3,
				"retry_reason": "rate_limit", "retry_delay_ms": 500,
			},
		})
		interaction.waitUIActorIdle()
	}

	postRetry(1)
	interaction.mu.Lock()
	started := interaction.dynamicStatusStarted
	interaction.mu.Unlock()
	require.False(t, started.IsZero(), "first retry must start the clock")

	time.Sleep(600 * time.Millisecond)
	postRetry(2)

	// 第二次 retry 不重置时钟：elapsed 必须继续累计（>= 600ms 而不是 0）。
	elapsed := interaction.dynamicStatusElapsedLocked(time.Now())
	if elapsed < 600*time.Millisecond {
		t.Fatalf("second retry reset the clock: elapsed=%v, want >= 600ms", elapsed)
	}

	interaction.mu.Lock()
	model := interaction.dynamicStatusModel
	interaction.mu.Unlock()
	if model == nil {
		t.Fatal("repeated retry must keep the dynamic model rendered")
	}
	plain := style.StatusLineDocument(*model, 160).PlainText()
	if !strings.Contains(plain, "Retrying step=1 attempt=2/3") {
		t.Fatalf("attempt field did not update on repeated retry: %q", plain)
	}
}

// Production-path regression: after a retry, a recovered provider can emit a
// long assistant stream. The unified reducer must mount the first mutable Scene
// cell once, then advance only AppState.Active for later chunks. Replacing the
// whole transcript for every chunk creates a full-frame action storm; the
// backend keeps reading tokens while the visible UI remains on "Retrying" until
// the final snapshot eventually catches up.
func TestLLMRetryUnifiedRendererProjectsDeltasBeforeFinal(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 18)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	require.True(t, interaction.enableUnifiedRendererWithWriter(&terminal), "attach unified renderer")

	bridge.BeginRun()
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	post := func(event runtimeevents.Event) {
		t.Helper()
		bridge.Handle(event)
		require.True(t, bridge.WaitForCurrentEvents(2*time.Second), "drain bridge event %s", event.Type)
		interaction.waitUIActorIdle()
		awaitUnifiedPresenterIdle(t, interaction)
	}
	post(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	post(runtimeevents.Event{
		Type:      "llm.retry",
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "step": 9, "attempt": 1, "max_attempts": 10,
			"retry_reason": "transport", "retry_delay_ms": 1073,
		},
	})

	// Reasoning uses the same mutable-cell projection as assistant output.
	// Cover it explicitly because many providers resume with hidden/summary
	// reasoning immediately after retrying, before the first answer token.
	terminal.Reset()
	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "llm_request_id": "request-1",
			"sequence": uint64(1), "mode": "append",
			"reasoning": map[string]interface{}{
				"format": "stream_delta", "summary": "checking recovery",
			},
		},
	})
	reasoningFirst := interaction.uiActor.AppState()
	require.Equal(t, scene.KindReasoning, reasoningFirst.Active.Kind)
	require.Equal(t, "checking recovery", reasoningFirst.Active.Source)
	require.Contains(t, terminal.String(), "checking recovery")
	reasoningTranscriptRevision := reasoningFirst.Transcript.Revision

	terminal.Reset()
	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "llm_request_id": "request-1",
			"sequence": uint64(2), "mode": "append",
			"reasoning": map[string]interface{}{
				"format": "stream_delta", "summary": " while streaming",
			},
		},
	})
	reasoningSecond := interaction.uiActor.AppState()
	require.Equal(t, "checking recovery while streaming", reasoningSecond.Active.Source)
	require.Equal(t, reasoningTranscriptRevision, reasoningSecond.Transcript.Revision,
		"later reasoning chunks must not enqueue a full ReplaceTranscript transaction")
	require.NotEmpty(t, terminal.String(),
		"the incremental reasoning frame must be flushed before assistant output")

	terminal.Reset()
	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "sequence": uint64(1),
			"mode": "append", "delta": "recovered output",
		},
	})
	first := interaction.uiActor.AppState()
	require.Equal(t, "recovered output", first.Active.Source)
	require.Equal(t, ui.ActiveCellMutable, first.Active.Phase)
	require.Contains(t, terminal.String(), "recovered output",
		"the first recovered chunk must reach the terminal before assistant.message")
	firstTranscriptRevision := first.Transcript.Revision

	terminal.Reset()
	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "sequence": uint64(2),
			"mode": "append", "delta": " is still streaming",
		},
	})
	second := interaction.uiActor.AppState()
	require.Equal(t, "recovered output is still streaming", second.Active.Source)
	require.Equal(t, firstTranscriptRevision, second.Transcript.Revision,
		"later chunks must not enqueue a full ReplaceTranscript transaction")
	require.NotEmpty(t, terminal.String(),
		"the incremental active-cell frame must be flushed before assistant.message")

	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "lead-session",
		TraceID:   "trace-1",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1",
			"content": "recovered output is still streaming",
		},
	})
	final := interaction.uiActor.AppState()
	require.Equal(t, ui.ActiveCellState{}, final.Active)
	require.Greater(t, final.Transcript.Revision, firstTranscriptRevision)
	require.Len(t, final.Transcript.Cells, 2)
	require.Equal(t, "checking recovery while streaming", final.Transcript.Cells[0].Source)
	require.Equal(t, "recovered output is still streaming", final.Transcript.Cells[1].Source)
}

func TestLLMRetryAfterRunEndDoesNotRestartCompletedStatus(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(100, 12)
	interaction.SetSurface(surface)

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.EndRun()
	interaction.waitUIActorIdle()

	interaction.mu.Lock()
	before := interaction.surfaceStatus
	beforeStarted := interaction.dynamicStatusStarted
	interaction.mu.Unlock()
	require.Equal(t, chatSurfaceStatusIdle, before.kind)
	require.True(t, beforeStarted.IsZero())

	// Transport callbacks from older/legacy providers may omit turn_id. The
	// late-run fence, rather than identity matching, must keep this live-only
	// state from rearming the completed composer.
	bridge.handleEvent(runtimeevents.Event{
		Type:      "llm.retry",
		SessionID: "lead-session",
		TraceID:   "late-trace",
		Payload: map[string]interface{}{
			"step": 9, "attempt": 2, "max_attempts": 10,
			"retry_reason": "transport", "retry_delay_ms": 1073,
		},
	})
	interaction.waitUIActorIdle()

	interaction.mu.Lock()
	after := interaction.surfaceStatus
	afterStarted := interaction.dynamicStatusStarted
	interaction.mu.Unlock()
	require.Equal(t, before, after)
	require.True(t, afterStarted.IsZero(), "late retry must not restart the status clock")
}

// L3 真实主循环 e2e：runChatLoop + os.Pipe 脚本注入 + 捕获真实渲染字节流，
// fake executor 在 turn 中经方案 B 结构化状态机入口（Interaction.SetRetrying，
// 等价于 chatRuntimeEventBridge 处理 llm.retry 事件后调用的同一入口）注入
// retry 状态，验证：
//  1. retry 状态行在真实循环中渲染 "Retrying step=... reason=..."（含
//     "(0s • esc to interrupt)" 起始时钟）；
//  2. 回复正文仍正常输出（retry 状态不得阻塞 turn）；
//  3. 主循环不卡死（时钟驱动的异步渲染路径不拖垮读行）。
//
// 注意：此测试刻意不挂 chatRuntimeEventBridge。挂载 bridge 会启用 UI actor
// 异步渲染路径，慢速 VM 下全屏渲染 action 风暴与主循环读行形成饥饿竞争
// （实测 40s+ 卡死）；event → bridge → UI actor → 时钟推进的完整桥接链路
// 已由上方非 TTY e2e（TestLLMRetryEventE2E_*）覆盖。
func TestTTY_LiveLoop_LLMRetryRendersAdvancingTimerE2E(t *testing.T) {
	const reply = "retry 后正常回复"
	run := runTTYLiveLoop(t, reply, []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, func(_ *ChatSession, ex *fakeChatExecutor) {
		// 方案 B 结构化状态机入口：直接在真实主循环内经 Interaction 公开
		// API SetRetrying 注入 retry 状态（与 bridge.handleEvent → reduce
		// 路径调用的同一函数）。不挂 chatRuntimeEventBridge：桥接全链路
		// （event → bridge → UI actor → 时钟推进）已由上方非 TTY e2e
		// 覆盖；挂载 bridge 会引入 actor 异步渲染与主循环读行在慢速 VM
		// 下的饥饿竞争（全屏渲染 action 风暴导致 40s 级卡死）。
		ex.onCall = func(ctx context.Context, session *ChatSession, _ string) (string, error) {
			session.Interaction.SetRetrying("step=1 attempt=2/3 reason=rate_limit")
			return reply, nil
		}
	})
	if !run.executor.called {
		t.Fatalf("fakeChatExecutor 未被调用（输入未进入真实循环）")
	}
	raw := run.raw
	if !strings.Contains(raw, "Retrying step=1 attempt=2/3") {
		t.Fatalf("llm.retry 未在真实主循环渲染重试详情: %q", raw)
	}
	if !strings.Contains(raw, "reason=rate_limit") {
		t.Fatalf("llm.retry 未渲染 retry_reason: %q", raw)
	}
	// 回复仍正常渲染（retry 状态不得阻塞正文输出）。
	if !strings.Contains(raw, reply) {
		t.Fatalf("回复未渲染: %q", raw)
	}
}
