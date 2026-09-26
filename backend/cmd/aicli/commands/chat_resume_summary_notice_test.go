package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 恢复在动态栏上的事件顺序：开始（恢复历史会话…）→ 进度（N/M）→ 装载历史 →
// 结束（恢复汇总）。结束事件必须等进度行退场后显现：抢跑会被随后的进度重绘覆盖。
func TestResumeSummaryNoticeFollowsProgressOnDynamicStatus(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	// 开始 + 进度：恢复进行中，秒表从恢复起点连续走。
	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 200, 250)
	coordinator.mu.Lock()
	coordinator.resumeProgress.started = time.Now().Add(-9 * time.Second)
	coordinator.repaintStatusModelsLocked()
	coordinator.mu.Unlock()
	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, "(9s)") {
		t.Fatalf("progress row must carry the running resume stopwatch, got %q", row)
	}

	if !coordinator.queueResumeSummaryNotice("已恢复历史会话: 读取文档（1轮/837条消息）\n") {
		t.Fatal("queueResumeSummaryNotice must accept the interactive dynamic bar")
	}
	// 进度期间汇总不得抢跑（否则会被随后的进度重绘覆盖）。
	if row := chatDynamicStatusRowText(coordinator, 160); strings.Contains(row, "已恢复历史会话") {
		t.Fatalf("summary must wait until the progress row retires, got %q", row)
	}

	coordinator.ClearChatResumeProgress()
	require.Eventually(t, func() bool {
		row := chatDynamicStatusRowText(coordinator, 160)
		return strings.Contains(row, "已恢复历史会话") &&
			strings.Contains(row, "1轮/837条消息") &&
			strings.Contains(row, "耗时")
	}, 5*time.Second, 20*time.Millisecond,
		"结束事件必须在动态栏上显现，并带整段恢复的耗时")
	// 耗时从整段恢复起点算起：9s 的秒表 + 采样时间，绝不是从加载结束重新计数。
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, "耗时9s") && !strings.Contains(row, "耗时10s") {
		t.Fatalf("summary elapsed must cover the whole resume, got %q", row)
	}
}

// 结束事件是临时事件：TTL 到期后自动让位，动态栏回到常规活动行/空闲态。
func TestResumeSummaryNoticeExpiresFromDynamicStatus(t *testing.T) {
	previousTTL := resumeSummaryNoticeTTL
	resumeSummaryNoticeTTL = 30 * time.Millisecond
	t.Cleanup(func() { resumeSummaryNoticeTTL = previousTTL })

	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	if !coordinator.queueResumeSummaryNotice("已恢复历史会话: 夹具（1轮/2条消息）") {
		t.Fatal("queueResumeSummaryNotice must accept the interactive dynamic bar")
	}
	require.Eventually(t, func() bool {
		return strings.Contains(chatDynamicStatusRowText(coordinator, 160), "已恢复历史会话")
	}, 3*time.Second, 10*time.Millisecond, "summary notice must appear")
	require.Eventually(t, func() bool {
		return !strings.Contains(chatDynamicStatusRowText(coordinator, 160), "已恢复历史会话")
	}, 5*time.Second, 20*time.Millisecond, "summary notice must expire")
}
