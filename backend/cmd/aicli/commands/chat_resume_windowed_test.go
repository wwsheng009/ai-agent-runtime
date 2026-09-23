package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestPrintPromptUnifiedPaintFailureKeepsPromptRetryable 固化 P0 修复：
// unified 生产路径的内容只能经 Scene/AppState 输出，PrintPrompt 的 legacy
// 写入在 unified 模式下是 no-op（writeTextLocked fail closed）。旧实现仍然把
// promptVisible 置为 true，于是「没画出来」被记成「已经画过」：此后每次
// PrintPrompt 都被 promptVisible 守卫直接拒绝，整个会话不再出现 composer
// （启动恢复时 fixed-bottom surface 尚未 attach 就是这种场景）。修复后该
// 路径不得置位，下一次尝试（或 actor 的 surface 路径）仍可渲染 prompt。
func TestPrintPromptUnifiedPaintFailureKeepsPromptRetryable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	// unified 模式且无 surface：legacy 写入 fail closed，不得标记为已绘制。
	coord.unifiedRenderer = true
	coord.PrintPrompt()
	if coord.promptVisible {
		t.Fatalf("unified 模式 no-op 绘制后不得把 prompt 标记为可见（否则整个会话不再重试 prompt）")
	}
	if coord.promptRenderedOnSurface {
		t.Fatalf("unified 模式 no-op 绘制后不得标记 promptRenderedOnSurface")
	}

	// 对照：同一 coordinator 关闭 unified 后必须照旧绘制并置位。
	coord.unifiedRenderer = false
	coord.PrintPrompt()
	if !coord.promptVisible {
		t.Fatalf("legacy 路径应正常绘制 prompt 并置位 promptVisible")
	}
	// terminalCaptureWriter 会裁掉行尾空格，因此比较去空白的 prompt 文本。
	if rendered := output.String(); !strings.Contains(rendered, strings.TrimSpace(ui.UserPromptText(0))) {
		t.Fatalf("expected legacy prompt in output, got %q", rendered)
	}
}

// TestPaintScheduledPromptFrameUnifiedPaintFailureKeepsPromptRetryable 固化
// 与 PrintPrompt 同源的第二个 fail-open 记账点：SchedulePromptRedraw 经
// render intent 投递 ui.Timer，reducer 落到 paintScheduledPromptFrame。
// 命令（/goal、/skill、/shell、!cmd 等 post-commit 发送）在回合边界调度该
// 重绘；unified 模式下 legacy 写入同样是 no-op，旧实现照样置位 promptVisible，
// 于是「命令回复后直接进入执行状态」的会话此后不再渲染任何输入行。
func TestPaintScheduledPromptFrameUnifiedPaintFailureKeepsPromptRetryable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{}
	coord := newTestChatInteractionCoordinator(t, session)
	coord.promptAdvanceFn = func() bool { return false }
	output := &terminalCaptureWriter{}
	coord.SetWriter(output)

	// reducer 入口的 seq 门禁：只有当前世代的重绘帧才允许绘制。
	scheduleFrame := func() {
		coord.mu.Lock()
		coord.promptSeq++
		seq := coord.promptSeq
		coord.mu.Unlock()
		coord.paintScheduledPromptFrame(seq)
	}

	// unified 模式且无 surface：不得把 no-op 记成已绘制。
	coord.unifiedRenderer = true
	scheduleFrame()
	if coord.promptVisible {
		t.Fatalf("unified 模式 no-op 重绘帧不得把 prompt 标记为可见（否则命令回合后输入区永久消失）")
	}
	if coord.promptRenderedOnSurface {
		t.Fatalf("unified 模式 no-op 重绘帧不得标记 promptRenderedOnSurface")
	}

	// 对照：同一 coordinator 关闭 unified 后必须照旧绘制并置位。
	coord.unifiedRenderer = false
	scheduleFrame()
	if !coord.promptVisible {
		t.Fatalf("legacy 路径应正常绘制 prompt 并置位 promptVisible")
	}
	if rendered := output.String(); !strings.Contains(rendered, strings.TrimSpace(ui.UserPromptText(0))) {
		t.Fatalf("expected legacy prompt in output, got %q", rendered)
	}
}

// TestStartupResumeWindowedHistoryLoadsNewestPageFirstThenBackfills 固化启动
// 恢复的「首屏窗口化」：同步只装载最新一页（composer 不必等待全量翻页），
// 较早的页由首帧之后的后台任务补齐，最终 ResumeHistory 仍是按时间升序的
// 完整 canonical 转录（与一次性同步装载的结果一致）。
func TestStartupResumeWindowedHistoryLoadsNewestPageFirstThenBackfills(t *testing.T) {
	storage, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      0,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	require.NoError(t, err)

	const messageCount = 150 // 超过单页（HistoryPageMessages=100）
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("assistant %d", index)))
		}
	}
	session.ReplaceHistory(messages)
	require.NoError(t, storage.Save(ctx, session))

	chatSession := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}

	// 窗口化第一步：最新一页同步就位，只有它挡在首帧之前。
	first, ok := loadNewestResumeHistoryPage(chatSession, session.ID)
	require.True(t, ok)
	require.True(t, first.HasMore, "%d 条消息应超过单页", messageCount)
	firstPage := append([]runtimetypes.Message(nil), chatSession.resumeHistorySnapshot()...)
	require.NotEmpty(t, firstPage)
	require.Less(t, len(firstPage), messageCount, "首帧前只应同步装载最新一页")

	// 首帧之后补齐：较早的页前插，最终得到完整升序转录。
	chatSession.deferResumeHistoryCompletion(session.ID, first.NextBeforeSeq)
	startDeferredResumeHistoryLoad(chatSession)
	require.Eventually(t, func() bool {
		return len(chatSession.resumeHistorySnapshot()) == messageCount
	}, 5*time.Second, 10*time.Millisecond, "较早的页应在首帧之后补齐为完整转录")

	history := chatSession.resumeHistorySnapshot()
	require.Equal(t, "user 0", history[0].Content)
	require.Equal(t, "assistant 149", history[messageCount-1].Content)
	// 前插不得重复或错位：已 seed 的最新页必须原样成为转录尾部。
	require.Equal(t, firstPage[0].Content, history[messageCount-len(firstPage)].Content)
	require.Equal(t, firstPage[len(firstPage)-1].Content, history[messageCount-1].Content)

	// 同步入口（会话内 /resume、窗口化关闭）保持一次性装载全量。
	syncSession := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	loadResumeCanonicalHistoryForStartup(syncSession, session.ID)
	require.Len(t, syncSession.resumeHistorySnapshot(), messageCount)
	require.Equal(t, "user 0", syncSession.resumeHistorySnapshot()[0].Content)
}
