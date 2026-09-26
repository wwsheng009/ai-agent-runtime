package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestChatResumeProgressRendersOnDynamicStatusRow 固化恢复进度的展示契约：
// 进度行整行落在 composer 动态栏（transient activity row）上，单行、带阶段名与
// 「已加载/总数」计数、带秒表；清除后动态栏回到空闲（不再残留进度文本）。
func TestChatResumeProgressRendersOnDynamicStatusRow(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	if !coordinator.ShowChatResumeProgress(ChatResumeProgress{
		Phase: chatResumeProgressPhaseRestore,
		Done:  200,
		Total: 250,
	}) {
		t.Fatal("interactive session must accept resume progress")
	}
	coordinator.mu.Lock()
	coordinator.resumeProgress.started = time.Now().Add(-12 * time.Second)
	coordinator.repaintStatusModelsLocked()
	model := coordinator.dynamicStatusModel
	coordinator.mu.Unlock()

	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "◦ 恢复历史会话 200/250") {
		t.Fatalf("progress phase/count missing from the dynamic status row: %q", row)
	}
	if !strings.Contains(row, "(12s)") {
		t.Fatalf("progress clock missing from the dynamic status row: %q", row)
	}
	if strings.ContainsAny(row, "\r\n") {
		t.Fatalf("resume progress must stay single-line: %q", row)
	}
	if model == nil || model.StateRole != style.RoleProgress {
		t.Fatalf("progress row must carry the progress role: %#v", model)
	}

	coordinator.ClearChatResumeProgress()
	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("cleared resume progress must release the dynamic status row: %q", row)
	}
	coordinator.mu.Lock()
	active := coordinator.resumeProgress
	coordinator.mu.Unlock()
	if active != nil {
		t.Fatalf("resume progress state leaked after clear: %#v", active)
	}
}

// TestChatResumeProgressUnknownTotalFallsBackToLoadedCount 固化 Total 缺失
// （存储未给总数）时的降级展示：只显示已加载条数，不得编造分母。
func TestChatResumeProgressUnknownTotalFallsBackToLoadedCount(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	coordinator.ShowChatResumeProgress(ChatResumeProgress{Phase: chatResumeProgressPhaseRestore, Done: 320})
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "恢复历史会话 已加载 320") {
		t.Fatalf("unknown-total progress must show the loaded count: %q", row)
	}
	if strings.Contains(row, "/") {
		t.Fatalf("unknown-total progress must not invent a denominator: %q", row)
	}
}

// TestChatResumeProgressSkipsNonInteractiveSessions 固化 plain/JSON 输出契约：
// 非交互 / JSON 会话不接受进度行，stdout 结构不因恢复进度改变。
func TestChatResumeProgressSkipsNonInteractiveSessions(t *testing.T) {
	for name, session := range map[string]*ChatSession{
		"non-interactive": {NoInteractive: true},
		"json-output":     {JSONOutput: true},
	} {
		coordinator := newTestChatInteractionCoordinator(t, session)
		session.Interaction = coordinator
		if coordinator.ShowChatResumeProgress(ChatResumeProgress{Phase: chatResumeProgressPhaseLoad}) {
			t.Fatalf("%s session accepted resume progress", name)
		}
		showChatResumeProgress(session, chatResumeProgressPhaseLoad, 1, 2)
		coordinator.mu.Lock()
		active := coordinator.resumeProgress
		coordinator.mu.Unlock()
		if active != nil {
			t.Fatalf("%s session retained resume progress state: %#v", name, active)
		}
	}
}

// TestChatResumeProgressYieldsToForegroundActivity 固化优先级：恢复进度是后台
// 任务，前台可见活动（流式输出等）必须优先；回到空闲后进度行复现，直到显式清除。
func TestChatResumeProgressYieldsToForegroundActivity(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	coordinator.ShowChatResumeProgress(ChatResumeProgress{Phase: chatResumeProgressPhaseRestore, Done: 3, Total: 9})
	coordinator.mu.Lock()
	coordinator.updateSurfaceStatusLocked(chatSurfaceStatus{kind: chatSurfaceStatusStreaming})
	coordinator.mu.Unlock()

	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "Generating response") {
		t.Fatalf("foreground activity must win over resume progress: %q", row)
	}
	if strings.Contains(row, "恢复历史会话") {
		t.Fatalf("resume progress must not cover a live turn: %q", row)
	}

	coordinator.mu.Lock()
	coordinator.updateSurfaceStatusLocked(chatSurfaceStatus{kind: chatSurfaceStatusIdle})
	coordinator.mu.Unlock()
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "恢复历史会话 3/9") {
		t.Fatalf("resume progress must return once the live turn goes idle: %q", row)
	}
}

// TestDeferredResumeHistoryBackfillShowsProgressOnDynamicStatusRow 是启动恢复
// （aicli resume / chat --resume）的后台补齐半程回归：首帧之后动态栏立即显示
// 「恢复历史会话 已加载/总数」，每读回一页推进计数，全部补齐后自动清除。
func TestDeferredResumeHistoryBackfillShowsProgressOnDynamicStatusRow(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	inner, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.CloseStorage() })
	gated := &gatedHistoryPagerStorage{
		SessionStorage: inner,
		gateCall:       3, // 最新页 + 第 2 页之后，第 3 次分页读取被挡住
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	var releaseOnce sync.Once
	openGate := func() { releaseOnce.Do(func() { close(gated.release) }) }
	defer openGate()

	manager := runtimechat.NewSessionManager(gated, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	runtimeSession, err := manager.Create(ctx, "tester")
	require.NoError(t, err)

	const messageCount = 250
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
	}
	runtimeSession.ReplaceHistory(messages)
	require.NoError(t, inner.Save(ctx, runtimeSession))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	first, ok := loadNewestResumeHistoryPage(session, runtimeSession.ID)
	require.True(t, ok)
	require.True(t, first.HasMore)
	session.deferResumeHistoryCompletionWithProgress(runtimeSession.ID, first.NextBeforeSeq, first.Total, len(first.Messages))
	startDeferredResumeHistoryLoad(session)

	// 首帧之后（最新页已装载）进度行必须立即可见，而不是等后台任务读完。
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "恢复历史会话 100/250") {
		t.Fatalf("startup resume progress missing before the backfill starts: %q", row)
	}

	select {
	case <-gated.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("较早页的分页读取没有推进到被挡住的第三页")
	}
	// 第 2 页已读回：计数推进到 200/250；第 3 页仍被挡住。
	require.Eventually(t, func() bool {
		return strings.Contains(chatDynamicStatusRowText(coordinator, 160), "恢复历史会话 200/250")
	}, 10*time.Second, 5*time.Millisecond, "每读回一页必须推进动态栏计数")
	// 进度行必须真的落在合成帧上（用户可见位置），而不只是缓存的模型。
	require.Eventually(t, func() bool {
		return strings.Contains(composedSurfaceFrameText(surface), "恢复历史会话 200/250")
	}, 10*time.Second, 5*time.Millisecond, "composer dynamic row must carry the resume progress text")
	// 统一渲染面（生产 presenter 的唯一数据源）的底部状态行必须同时带上进度：
	// 历史页发布后 AppState 底部保留区仍要给进度行留出那一行，历史行不得占用。
	require.Eventually(t, func() bool {
		actor := coordinator.ensureUIActor()
		if actor == nil {
			return false
		}
		model := actor.AppState().Bottom.DynamicStatusModel
		return model != nil && strings.Contains(model.StateText, "恢复历史会话 200/250")
	}, 10*time.Second, 5*time.Millisecond, "unified AppState bottom pane must carry the resume progress row")

	openGate()
	require.Eventually(t, func() bool {
		return len(session.resumeHistorySnapshot()) == messageCount
	}, 10*time.Second, 10*time.Millisecond, "最后一个更早的页也必须在门放开后补齐")
	// 补齐完成后收尾序列只登记异步全量重投递，动态行必须继续有内容（装载历史），
	// 而不是整段空白；有界窗口到期后才彻底清除、回到空闲。
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		state := coordinator.resumeProgress
		coordinator.mu.Unlock()
		return state != nil && state.loadSettle
	}, 10*time.Second, 5*time.Millisecond, "补齐完成后动态行必须进入装载收尾阶段")
	require.Eventually(t, func() bool {
		return strings.Contains(chatDynamicStatusRowText(coordinator, 160), chatResumeProgressPhaseSettle)
	}, 10*time.Second, 5*time.Millisecond, "收尾阶段动态栏必须显示装载历史")
	coordinator.mu.Lock()
	if coordinator.resumeProgress != nil {
		coordinator.resumeProgress.started = time.Now().Add(-chatHistoryLoadSettleLimit - time.Second)
	}
	coordinator.repaintStatusModelsLocked()
	coordinator.mu.Unlock()
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return coordinator.resumeProgress == nil && coordinator.dynamicStatusModel == nil
	}, 10*time.Second, 5*time.Millisecond, "收尾窗口到期后动态栏必须回到空闲")
}

// TestStartDeferredResumeHistoryLoadClearsOrphanProgress 固化无补齐任务时的
// 收尾：replayLoadedSessionHistory 在同步回放期间亮出的进度行必须被无条件清除，
// 否则单页历史会把「恢复历史会话…」永久留在动态栏。
func TestStartDeferredResumeHistoryLoadClearsOrphanProgress(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 0, 0)
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "恢复历史会话") {
		t.Fatalf("progress must be visible before the no-op backfill: %q", row)
	}

	startDeferredResumeHistoryLoad(session)

	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("no-op backfill must clear the replay progress row: %q", row)
	}
}

// TestCompleteDeferredResumeHistoryLoadKeepsProgressUntilFinalRender 固化补齐段
// 的收尾顺序契约：全量 seed（补齐段最重的一次历史渲染，会与底部保留区同帧
// 重排）期间进度行必须保持在位；收尾渲染只是登记异步重投递，因此结束后动态行
// 交给「装载历史」，直到收尾窗口到期或有真实前台活动接手才彻底清除。
// 提前清除会让历史区域立刻多出一行、占掉进度行原来的位置——用户看到的就是
// 「历史消息渲染覆盖动态状态栏」以及收尾阶段动态行整段空白。
func TestCompleteDeferredResumeHistoryLoadKeepsProgressUntilFinalRender(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 200, 250)
	renderCalls := 0
	completeDeferredResumeHistoryLoad(session, 2, nil, func() {
		renderCalls++
		if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "恢复历史会话 200/250") {
			t.Fatalf("progress row must stay visible during the final full-history render: %q", row)
		}
	})
	if renderCalls != 1 {
		t.Fatalf("final render must run exactly once, got %d", renderCalls)
	}
	// 收尾阶段（异步全量重投递）动态行必须继续有内容：进度行让位给「装载历史」。
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, chatResumeProgressPhaseSettle) {
		t.Fatalf("dynamic row must stay in the settle phase after the final render: %q", row)
	}
	// 窗口到期后就地清除（有界，不会永久停在装载阶段）。
	coordinator.mu.Lock()
	if coordinator.resumeProgress == nil || !coordinator.resumeProgress.loadSettle {
		coordinator.mu.Unlock()
		t.Fatalf("completion must hand the dynamic row to the load-settle phase")
	}
	coordinator.resumeProgress.started = time.Now().Add(-chatHistoryLoadSettleLimit - time.Second)
	coordinator.repaintStatusModelsLocked()
	coordinator.mu.Unlock()
	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("settle row must clear once its bounded window expires: %q", row)
	}
}

// TestLoadRuntimeConversationHandsProgressToFollowUpStage 固化装载阶段的交接规则：
// 会话有可见转录（后续 replayLoadedSessionHistory 会接手）时不得提前清除进度行，
// 否则底部保留区先收缩一行，历史内容正好落进进度行的位置；只有确定没有后续阶段
// （空会话）时才在装载阶段收尾。
func TestLoadRuntimeConversationHandsProgressToFollowUpStage(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	target, err := manager.Create(ctx, "tester")
	require.NoError(t, err)
	target.ReplaceHistory([]runtimetypes.Message{*runtimetypes.NewUserMessage("restore me")})
	require.NoError(t, manager.Update(ctx, target))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	require.True(t, coordinator.enableUnifiedRendererWithWriter(&terminal))

	require.NoError(t, loadRuntimeConversation(session, target.ID))
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "恢复历史会话") {
		t.Fatalf("load must hand the progress row to the follow-up replay stage: %q", row)
	}

	// 后续阶段收尾（这里等价于回放之后的 no-op 补齐启动）负责清除进度行。
	startDeferredResumeHistoryLoad(session)
	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("follow-up completion must clear the progress row: %q", row)
	}
}

// TestLoadRuntimeConversationClearsProgressWithoutUnifiedFollowUp 固化 legacy
// 会话的收尾：没有统一渲染的「回放 + 后台补齐」接手链，装载阶段必须自己清除
// 进度行，否则状态栏会永久停在「恢复历史会话」。
func TestLoadRuntimeConversationClearsProgressWithoutUnifiedFollowUp(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	target, err := manager.Create(ctx, "tester")
	require.NoError(t, err)
	target.ReplaceHistory([]runtimetypes.Message{*runtimetypes.NewUserMessage("restore me")})
	require.NoError(t, manager.Update(ctx, target))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	require.NoError(t, loadRuntimeConversation(session, target.ID))
	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("legacy load has no follow-up stage and must clear the progress row: %q", row)
	}
}

// TestLoadRuntimeConversationClearsProgressForEmptySession 固化空会话的收尾：
// 没有可见转录、也没有待补齐页时，装载阶段必须自己清除进度行，不得残留。
func TestLoadRuntimeConversationClearsProgressForEmptySession(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	t.Cleanup(manager.Stop)

	ctx := context.Background()
	target, err := manager.Create(ctx, "tester")
	require.NoError(t, err)
	target.ReplaceHistory([]runtimetypes.Message{{Role: "system", Content: "placeholder", Metadata: runtimetypes.NewMetadata()}})
	require.NoError(t, manager.Update(ctx, target))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester"}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	require.NoError(t, loadRuntimeConversation(session, target.ID))
	if row := chatDynamicStatusRowText(coordinator, 160); row != "" {
		t.Fatalf("empty session must not leave a resume progress row behind: %q", row)
	}
}
