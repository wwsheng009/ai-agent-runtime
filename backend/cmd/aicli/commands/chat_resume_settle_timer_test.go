package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// 「装载历史」起于加载完成之后：它不得再挂一个秒表（"加载完成才开始计时"没有
// 意义，且重投递期间动态栏 tick 被投递压力抑制，数字会冻在某个值上误导用户）。
func TestChatResumeLoadSettleHasNoStopwatch(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)

	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 200, 250)
	coordinator.mu.Lock()
	if coordinator.resumeProgress == nil {
		coordinator.mu.Unlock()
		t.Fatal("resume progress state missing")
	}
	// 恢复阶段已经跑了 7s（模拟真实的加载耗时）。
	coordinator.resumeProgress.started = time.Now().Add(-7 * time.Second)
	coordinator.mu.Unlock()

	markChatHistoryLoadPending(session)
	row := chatDynamicStatusRowText(coordinator, 160)
	if !strings.Contains(row, chatResumeProgressPhaseSettle) {
		t.Fatalf("settle row missing: %q", row)
	}
	if strings.Contains(row, "(0s)") || strings.Contains(row, "(7s)") {
		t.Fatalf("settle row must not carry a stopwatch, got %q", row)
	}
	if !strings.Contains(row, chatResumeProgressPhaseSettle+"…") {
		t.Fatalf("settle row must be plain phase text without suffix, got %q", row)
	}
	// 有界兜底窗口从收尾段重新起算：长恢复不能因为总时长超窗而立刻撤行。
	coordinator.mu.Lock()
	settleWindowStart := coordinator.resumeProgress.started
	coordinator.mu.Unlock()
	if time.Since(settleWindowStart) > time.Second {
		t.Fatalf("settle window must restart at the settle hand-off, got start %v", settleWindowStart)
	}
}

// 收尾重投递一旦落地（授权已消费、账本无未完成提交），「装载历史」行必须立即
// 撤掉；30s 有界窗口只是投递失败/挂起时的兜底，不是正常路径的停留时长。
func TestChatResumeLoadSettleClearsOnceDeliverySettles(t *testing.T) {
	session := &ChatSession{}
	coordinator := newTestChatInteractionCoordinator(t, session)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(160, 12)
	coordinator.SetSurface(surface)
	// 生产路径上收尾重投递必然经由 ui actor；未挂载 actor 时探测会自行跳过。
	require.NotNil(t, coordinator.ensureUIActor(), "ui actor must be attachable")

	showChatResumeProgress(session, chatResumeProgressPhaseRestore, 200, 250)
	markChatHistoryLoadPending(session)
	settleChatHistoryLoadWhenDelivered(session)

	if row := chatDynamicStatusRowText(coordinator, 160); !strings.Contains(row, chatResumeProgressPhaseSettle) {
		t.Fatalf("settle row must be visible before the delivery settles: %q", row)
	}
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return coordinator.resumeProgress == nil && coordinator.dynamicStatusModel == nil
	}, 5*time.Second, 25*time.Millisecond,
		"装载历史行必须在重投递落地后立即清除，而不是等满有界窗口")
	if row := chatDynamicStatusRowText(coordinator, 160); strings.Contains(row, chatResumeProgressPhaseSettle) {
		t.Fatalf("settle row must not linger after the delivery settled: %q", row)
	}
}
