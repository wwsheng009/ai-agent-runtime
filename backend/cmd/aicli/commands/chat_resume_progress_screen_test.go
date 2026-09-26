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
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newResumeProgressVtGateway 构造生产交付路径的观察面：
// PhysicalSink(MemorySink) + VT mirror（终端解释器看到什么）。
func newResumeProgressVtGateway(t *testing.T) (*outputpkg.RenderOutputGateway, *outputpkg.VirtualTerminalSink, *outputpkg.MemorySink) {
	t.Helper()
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "resume-progress-memory",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	emu := ui.NewVtTerminalEmulator()
	virtual := outputpkg.NewVirtualTerminalSink("pt-virtual", emu, outputpkg.VirtualSinkOptions{})
	gw, err := outputpkg.NewRenderOutputGateway("resume-progress-vt", outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   64,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 512, MaxBytes: 4 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 512, MaxBytes: 4 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 64,
	}, outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []outputpkg.RenderMirror{{
			Sink:      virtual,
			Policy:    outputpkg.MirrorBestEffort,
			ApplyMode: outputpkg.MirrorApplyBytes,
			Ownership: outputpkg.SinkOwned,
			Timeout:   2 * time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return gw, virtual, sink
}

func resumeProgressVisibleScreen(t *testing.T, coordinator *chatInteractionCoordinator, gw *outputpkg.RenderOutputGateway, virtual *outputpkg.VirtualTerminalSink) []string {
	t.Helper()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	return virtual.Projection().Rows
}

// snippetBytes 把一批终端字节截成可读片段，便于在断言失败时定位批次内容。
func snippetBytes(value []byte) string {
	const limit = 240
	if len(value) > limit {
		value = value[:limit]
	}
	return fmt.Sprintf("%q", string(value))
}

// TestResumeProgressDynamicRowSurvivesIncrementalHistoryPublishOnScreen 用 VT
// 解释器核对“用户实际看到的屏幕”：较早页逐页发布（历史消息重渲染）期间，动态栏的
// 恢复进度行必须保持在位，且进度行以下不得出现历史正文。
//
// 这是「历史消息渲染覆盖动态状态栏位置」的物理帧回归：先前的断言只看合成帧 /
// AppState，看不到 TerminalSession 的 differential 重绘是否会漏掉底部保留区。
func TestResumeProgressDynamicRowSurvivesIncrementalHistoryPublishOnScreen(t *testing.T) {
	oldInteractive := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { chatIsInteractiveTerminal = oldInteractive })
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	gw, virtual, sink := newResumeProgressVtGateway(t)

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
		messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("history-probe %d", index)))
	}
	runtimeSession.ReplaceHistory(messages)
	require.NoError(t, inner.Save(ctx, runtimeSession))

	session := &ChatSession{SessionManager: manager, SessionUserID: "tester", Stream: true}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	require.True(t, coordinator.enableUnifiedRendererWithPort(gw), "unified renderer must attach to the production gateway port")
	// 与真实启动一致：composer 先钉住（底部保留区含输入行），随后才是进度行与历史。
	presentStartupInteractiveComposer(session)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	first, ok := loadNewestResumeHistoryPage(session, runtimeSession.ID)
	require.True(t, ok)
	require.True(t, first.HasMore)
	session.deferResumeHistoryCompletionWithProgress(runtimeSession.ID, first.NextBeforeSeq, first.Total, len(first.Messages))
	startDeferredResumeHistoryLoad(session)

	select {
	case <-gated.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("较早页的分页读取没有推进到被挡住的第三页")
	}
	require.Eventually(t, func() bool {
		return strings.Contains(chatDynamicStatusRowText(coordinator, 160), "恢复历史会话 200/250")
	}, 10*time.Second, 5*time.Millisecond, "每读回一页必须推进动态栏计数")

	rows := resumeProgressVisibleScreen(t, coordinator, gw, virtual)
	for index, row := range rows {
		t.Logf("screen[%02d]=%q", index+1, row)
	}
	progressIndex := -1
	for index, row := range rows {
		if strings.Contains(row, "恢复历史会话 200/250") {
			progressIndex = index
		}
	}
	require.GreaterOrEqual(t, progressIndex, 0, "逐页发布期间动态进度行必须留在可见屏幕上")
	for index := progressIndex + 1; index < len(rows); index++ {
		require.NotContains(t, rows[index], "history-probe",
			"进度行以下（row %d）不得出现历史正文：%q", index+1, rows[index])
	}
	// 进度行下面必须仍然是底部保留区（输入/会话/状态行），不能被历史占据。
	require.NotEmpty(t, rows[len(rows)-1], "最后一行必须仍是持久状态行")
	require.NotContains(t, rows[len(rows)-1], "history-probe")

	// 逐批回放物理帧（当前窗口内不得发生清除）：进度行一旦出现，后续任何批次都不
	// 得让它在屏幕上消失——这正是「恢复进行中进度行被历史盖住/抖动」的物理判据。
	stableBatches := sink.SnapshotBatches()
	replay := ui.NewVtTerminalEmulator()
	progressAppearedAt := -1
	for index, batch := range stableBatches {
		if len(batch.Bytes) == 0 {
			continue
		}
		if err := replay.Apply(batch.Bytes); err != nil {
			t.Fatalf("replay batch %d (%s): %v", index, batch.Kind, err)
		}
		hasProgress := false
		for _, row := range replay.Snapshot().Rows {
			if strings.Contains(row, "恢复历史会话") {
				hasProgress = true
				break
			}
		}
		if hasProgress {
			if progressAppearedAt < 0 {
				progressAppearedAt = index
			}
			continue
		}
		if progressAppearedAt >= 0 {
			t.Fatalf("batches[%d] (%s) removed the resume progress row from the physical screen (first shown at batch %d): %q",
				index, batch.Kind, progressAppearedAt, snippetBytes(batch.Bytes))
		}
	}
	require.GreaterOrEqual(t, progressAppearedAt, 0, "物理帧序列里必须出现过进度行")
	// 更强的批次契约：任何携带历史字节的事务都必须在同一批里整段重绘底部保留区
	// （含进度行）。差分器只描述应然模型，历史字节写入之后保留区必须被显式恢复。
	for index, batch := range stableBatches {
		if len(batch.Bytes) == 0 || !bytes.Contains(batch.Bytes, []byte("history-probe")) {
			continue
		}
		require.Contains(t, string(batch.Bytes), "恢复历史会话",
			"batches[%d] (%s) 写入历史字节却没有同批重绘底部保留区（进度行）", index, batch.Kind)
	}

	openGate()
	require.Eventually(t, func() bool {
		return len(session.resumeHistorySnapshot()) == messageCount
	}, 10*time.Second, 10*time.Millisecond, "最后一个更早的页也必须在门放开后补齐")
	// 收尾序列只登记异步全量重投递：动态行交给「装载历史」继续保持可见，
	// 而不是就地清空（那段异步重投递才是整段装载里最长的部分）。
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		state := coordinator.resumeProgress
		coordinator.mu.Unlock()
		return state != nil && state.loadSettle
	}, 10*time.Second, 5*time.Millisecond, "补齐完成后动态行必须进入装载收尾阶段")

	rows = resumeProgressVisibleScreen(t, coordinator, gw, virtual)
	for index, row := range rows {
		t.Logf("final screen[%02d]=%q", index+1, row)
	}
	for _, row := range rows {
		require.NotContains(t, row, "恢复历史会话", "补齐完成后不得残留进度文本：%q", row)
	}
	settleVisible := false
	for _, row := range rows {
		if strings.Contains(row, chatResumeProgressPhaseSettle) {
			settleVisible = true
		}
	}
	require.True(t, settleVisible, "收尾阶段动态行必须仍显示装载历史：%q", rows)
	require.NotEmpty(t, rows[len(rows)-1], "补齐完成后最后一行必须仍是持久状态行")

	// 收尾窗口：授权式 reset（\x1b[2J\x1b[3J）会清掉整屏（含 composer band），
	// 这类事务必须在同一批里把 composer（状态行/输入行）整段重画回来。
	postBatches := sink.SnapshotBatches()
	for index, batch := range postBatches[len(stableBatches):] {
		if len(batch.Bytes) == 0 {
			continue
		}
		if !bytes.Contains(batch.Bytes, []byte("\x1b[2J")) {
			continue
		}
		require.Contains(t, string(batch.Bytes), "Plan OFF",
			"batches[stable+%d] (%s) 清屏却没有同批重画 composer 状态行", index, batch.Kind)
	}
}
