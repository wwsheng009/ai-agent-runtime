package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestTrimResumeHistoryAfterDeliveryCapsAtFixedLimit 锁定展示历史的内存上限：
//
// 背景（2026-10-21 生产诊断）：屏幕上真正绘制的只有 FixedBottomSurface 的
// historyWindow（硬上限 historyWindowMaxLines = 400 行），而 ResumeHistory 却
// 整份驻留 canonical 转录（该会话 18,677 条）。两者数量级脱钩，构成 157 MB
// Go 堆中无法归因的部分之一。
//
// 本测试锁定：装载交付完成后，展示历史必须被裁剪到固定上限，且��销只作用于
// 展示快照（模型上下文 session.Messages 不受影响）。
func TestTrimResumeHistoryAfterDeliveryCapsAtFixedLimit(t *testing.T) {
	const total = 1500
	session := &ChatSession{}
	history := make([]runtimetypes.Message, 0, total)
	for index := 0; index < total; index++ {
		if index%2 == 0 {
			history = append(history, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		} else {
			history = append(history, *runtimetypes.NewAssistantMessage(fmt.Sprintf("assistant %d", index)))
		}
	}
	session.setResumeHistory(history)
	// 模型上下文单独一份，用于证明裁剪不会碰到它。
	modelMessages := []runtimetypes.Message{*runtimetypes.NewUserMessage("model only")}
	session.Messages = modelMessages

	trimResumeHistoryAfterDelivery(session)

	displayed := session.resumeHistorySnapshot()
	require.Len(t, displayed, resumeHistoryRetainedAfterDelivery,
		"展示历史必须被裁剪到固定上限，否则 18k 条会整份常驻")
	require.Equal(t, "assistant 1499", displayed[len(displayed)-1].Content,
		"裁剪必须保留最新尾部")
	require.Equal(t, fmt.Sprintf("user %d", total-resumeHistoryRetainedAfterDelivery), displayed[0].Content,
		"裁剪后的首条必须正好是保留窗口的起点")

	trimCount, trimmedOff := resumeHistoryTrimStats(session)
	require.Equal(t, uint64(1), trimCount, "裁剪必须留下可观测痕迹（降级不得静默）")
	require.Equal(t, total-resumeHistoryRetainedAfterDelivery, trimmedOff)

	require.Equal(t, modelMessages, session.Messages,
		"裁剪只作用于展示快照，绝不改动模型上下文")
	require.True(t, session.resumeHistoryTruncated())
}

// TestTrimResumeHistorySkippedWhileBackfillPending 锁定安全前提：
// 后台补齐仍在进行（或仍登记了待补齐游标）时绝不能裁剪，否则尚未重放的
// 较早页会永久丢失。
func TestTrimResumeHistorySkippedWhileBackfillPending(t *testing.T) {
	const total = 1500
	session := &ChatSession{}
	history := make([]runtimetypes.Message, 0, total)
	for index := 0; index < total; index++ {
		history = append(history, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
	}
	session.setResumeHistory(history)

	// 模拟补齐仍在飞：交付挂起 + 待补齐游标。
	session.deferResumeHistoryCompletion("session-x", 42)
	require.True(t, session.resumeHistoryBackfillInFlightNow())

	trimResumeHistoryAfterDelivery(session)

	require.Len(t, session.resumeHistorySnapshot(), total,
		"补齐在飞时不得裁剪：尚未重放的页会永久丢失")
	require.False(t, session.resumeHistoryTruncated())
}

// TestTrimResumeHistoryKeepsWindowWhenAlreadySmall 确认裁剪是幂等且不误伤：
// 已经在上限内的展示历史不应被标记为"已裁剪"。
func TestTrimResumeHistoryKeepsWindowWhenAlreadySmall(t *testing.T) {
	session := &ChatSession{}
	history := make([]runtimetypes.Message, 0, 10)
	for index := 0; index < 10; index++ {
		history = append(history, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
	}
	session.setResumeHistory(history)

	trimResumeHistoryAfterDelivery(session)

	require.Len(t, session.resumeHistorySnapshot(), 10)
	require.False(t, session.resumeHistoryTruncated(), "未超上限时不应标记为已裁剪")
	trimCount, _ := resumeHistoryTrimStats(session)
	require.Zero(t, trimCount)
}

// TestTrimResumeHistoryAlignsToTurnBoundary 锁定裁剪边界：不得把保留窗口的
// 起点落在 tool 结果中间，否则重放会把半截 tool 链画成孤立行。
func TestTrimResumeHistoryAlignsToTurnBoundary(t *testing.T) {
	session := &ChatSession{}
	history := make([]runtimetypes.Message, 0, 1000)
	// 构造长串 tool 结果，使默认切点正好落在 tool 行上。
	for index := 0; index < 500; index++ {
		history = append(history, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		history = append(history, *runtimetypes.NewToolMessage(fmt.Sprintf("call-%d", index), "tool output"))
	}
	session.setResumeHistory(history)

	trimResumeHistoryAfterDelivery(session)

	displayed := session.resumeHistorySnapshot()
	require.NotEmpty(t, displayed)
	require.NotEqual(t, "tool", displayed[0].Role,
		"裁剪起点必须对齐到完整轮次，不能从 tool 结果中间开始")
}

// TestRefreshResumeHistoryForReplayRestoresFullTranscript 锁定方案 2 的核心
// 承诺：裁剪之后，需要完整历史的重放入口（/history）能从存储重新分页读回，
// 用户看到的仍是完整转录，而不是静默的尾部窗口。
func TestRefreshResumeHistoryForReplayRestoresFullTranscript(t *testing.T) {
	ctx := context.Background()
	inner, err := runtimechat.NewSQLiteSessionStorage(runtimechat.DefaultPersistentSessionStorageConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.CloseStorage() })
	manager := runtimechat.NewSessionManager(inner, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	const messageCount = 700 // > 512 保留上限，且 > 单页 100
	messages := make([]runtimetypes.Message, 0, messageCount)
	for index := 0; index < messageCount; index++ {
		if index%2 == 0 {
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("user %d", index)))
		} else {
			messages = append(messages, *runtimetypes.NewAssistantMessage(fmt.Sprintf("assistant %d", index)))
		}
	}
	runtimeSession, err := manager.Create(ctx, "tester")
	require.NoError(t, err)
	runtimeSession.ReplaceHistory(messages)
	require.NoError(t, inner.Save(ctx, runtimeSession))

	chatSession := &ChatSession{SessionManager: manager, SessionUserID: "tester", NoInteractive: true}
	chatSession.RuntimeSession = runtimeSession

	// 先装载完整展示历史，再裁剪。
	first, ok := loadNewestResumeHistoryPage(chatSession, runtimeSession.ID)
	require.True(t, ok)
	fetch := fetchOlderResumeHistoryPages(ctx, manager, runtimeSession.ID, first.NextBeforeSeq)
	require.NotEmpty(t, fetch)
	require.True(t, chatSession.prependResumeHistoryPages(chatSession.resumeHistoryGenerationValue(), fetch))
	require.Len(t, chatSession.resumeHistorySnapshot(), messageCount)

	trimResumeHistoryAfterDelivery(chatSession)
	require.True(t, chatSession.resumeHistoryTruncated())
	require.Len(t, chatSession.resumeHistorySnapshot(), resumeHistoryRetainedAfterDelivery)

	// /history 语义：必须能恢复完整转录。
	require.True(t, refreshResumeHistoryForReplay(chatSession),
		"裁剪后必须能从存储重新装载完整历史")
	require.Len(t, chatSession.resumeHistorySnapshot(), messageCount,
		"重新装载后必须是完整转录，不得只显示保留窗口")
	require.False(t, chatSession.resumeHistoryTruncated(),
		"重新装载后裁剪标记必须清除")

	restored := chatSession.resumeHistorySnapshot()
	require.Equal(t, "user 0", restored[0].Content, "完整历史必须从最早一条开始")
	require.Equal(t, fmt.Sprintf("assistant %d", messageCount-1), restored[len(restored)-1].Content)
}

// TestRefreshResumeHistoryFailsClosedWithoutPager 锁定降级路径：没有分页后端
// 时无法重新装载，必须返回 false 让调用方显式标注，而不是静默只显示窗口。
func TestRefreshResumeHistoryFailsClosedWithoutPager(t *testing.T) {
	chatSession := &ChatSession{}
	require.False(t, refreshResumeHistoryForReplay(chatSession),
		"无 SessionManager 时必须 fail-closed，由调用方标注降级")
}
