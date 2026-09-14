package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// TestChatDebugTurnMetricsBlockShowsBridgeWatermarkWithoutObserve 覆盖 §6.4 落点 C：
// /debug/chat/status 必须能读出 turn 级指标——通告时刻水位来自事件桥（与 TUI
// 状态行同源），终局水位来自 observe 采集器；观测不可用时降级为占位符而不是报错。
func TestChatDebugTurnMetricsBlockShowsBridgeWatermarkWithoutObserve(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const (
		sessionID = "session-turn-debug"
		turnID    = "turn-turn-debug"
		line      = "turn budget: step 240/300 · tokens 84%"
	)
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	session.RuntimeEventBridge = newTurnBudgetTestBridge(t, sessionID, turnID)
	deliverTurnBudgetEvent(session.RuntimeEventBridge, turnBudgetReminderEvent(sessionID, turnID, "soft", line, nil))

	plain := renderDocPlainText(buildChatDebugDisplayDocument(session))
	for _, marker := range []string{
		"Turn Budget / Lifecycle:",
		"Turn Budget Level:",
		"Turn Budget Line:",
		"Turn Budget Ratio:",
		line,
		"0.800",
	} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("turn 区块缺少锚点 %q\n---\n%s", marker, plain)
		}
	}
	// 该 session 没有 LocalRuntimeHost：observe 服务不可用，区块必须降级说明，
	// 而不是让 /debug 报错或静默省略整节。
	for _, marker := range []string{"Turns Running:", "Last Turn:", "<observe disabled>"} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("观测不可用时缺少占位锚点 %q\n---\n%s", marker, plain)
		}
	}
}

// TestFormatChatDebugTurnWatermark 固定终局水位的渲染口径：finished 给 elapsed，
// 只有 started 时按 running 呈现（不能把 step=0 误读成"跑了 0 步"），
// 未配置步数上限时显示 unlimited。
func TestFormatChatDebugTurnWatermark(t *testing.T) {
	finished := &runtimeobserve.TurnSummary{
		SessionID:   "s",
		Step:        4,
		MaxSteps:    10,
		ElapsedMS:   90000,
		BudgetLevel: "soft",
		BudgetRatio: 0.84,
		FinishedAt:  time.Unix(1700000000, 0).UTC(),
	}
	require.Equal(t, "session=s step=4/10 elapsed=1m30s level=soft ratio=0.84", formatChatDebugTurnWatermark(finished))

	running := &runtimeobserve.TurnSummary{SessionID: "s", MaxSteps: 10, BudgetLevel: "ok"}
	require.Equal(t, "session=s step=0/10 state=running level=ok", formatChatDebugTurnWatermark(running))

	// 无会话 id（只看到 finished 的异常顺序）时不输出空字段。
	unlimited := &runtimeobserve.TurnSummary{Step: 3, ElapsedMS: 1500, FinishedAt: time.Unix(1700000000, 0).UTC()}
	require.Equal(t, "step=3/unlimited elapsed=2s", formatChatDebugTurnWatermark(unlimited))

	require.Equal(t, "<none observed>", formatChatDebugTurnWatermark(nil))
}
