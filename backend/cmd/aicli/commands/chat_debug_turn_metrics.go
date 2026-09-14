package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// chatDebugTurnSnapshotTimeout 限制 debug 区块读取 observe 快照的时长：
// /debug 渲染路径不能因为观测侧繁忙而挂住。
const chatDebugTurnSnapshotTimeout = 2 * time.Second

// appendChatDebugTurnMetricsLines 输出 /debug/chat/status 的 turn 级指标
// （docs/plan/ui-event-bridge-drop-hardening.md §6.4 落点 C）。
//
// 两类数据刻意分开，避免把"通告时刻"当成"终局"：
//   - Turn Budget 行来自事件桥原子快照，是**通告时刻**的水位（与 TUI 状态行同源，
//     取样于软着陆触发那一步）；
//   - Turns Running / Last Turn 来自 observe 采集器对 agent.turn.started/finished
//     的聚合，是**终局**水位（最近一轮的 step/elapsed_ms/budget_*）。
//
// 观测未启用或无会话时只显示占位符，不报错：该区块是只读辅助信息。
func appendChatDebugTurnMetricsLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if builder == nil {
		return
	}
	builder.heading("Turn Budget / Lifecycle: (GET /debug/chat/status#turn)")
	if session == nil || session.RuntimeEventBridge == nil {
		builder.meta("Turn Budget:", "<none this run>")
	} else if snap, ok := session.RuntimeEventBridge.TurnBudgetSnapshot(); ok {
		builder.meta("Turn Budget Level:", chatDebugValueOrNone(snap.Level))
		builder.meta("Turn Budget Line:", chatDebugValueOrNone(snap.Line))
		builder.meta("Turn Budget Ratio:", strconv.FormatFloat(snap.Ratio, 'f', 3, 64))
	} else {
		builder.meta("Turn Budget:", "<none this run>")
	}

	// 用被渲染 session 自己的 host 建服务（而不是全局活动会话），这样
	// /debug/chat/status 的 turn 区块与文档其余部分指向同一个会话。
	svc := ensureLocalObserveService(session.LocalRuntimeHost)
	if svc == nil {
		builder.meta("Turns Running:", "<observe disabled>")
		builder.meta("Last Turn:", "<observe disabled>")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatDebugTurnSnapshotTimeout)
	defer cancel()
	snapshot, err := svc.BuildSnapshot(ctx, false)
	if err != nil {
		builder.meta("Turns Running:", "<snapshot unavailable>")
		builder.meta("Last Turn:", chatDebugValueOrNone(err.Error()))
		return
	}
	builder.meta("Turns Running:", strconv.Itoa(snapshot.Runtime.RunningTurns))
	turn := snapshot.Runtime.LastTurn
	if turn == nil {
		builder.meta("Last Turn:", "<none observed>")
		return
	}
	builder.meta("Last Turn:", formatChatDebugTurnWatermark(turn))
	if !turn.FinishedAt.IsZero() {
		builder.meta("Last Turn Finished:", turn.FinishedAt.UTC().Format(time.RFC3339))
	}
}

// formatChatDebugTurnWatermark 渲染最近一轮的终局水位（与 observe 快照字段同源）。
// 未观测到 finished（只有 started）时给出 running 语义，避免把 step=0 误读成"跑了 0 步"。
func formatChatDebugTurnWatermark(turn *runtimeobserve.TurnSummary) string {
	if turn == nil {
		return "<none observed>"
	}
	parts := make([]string, 0, 6)
	if turn.SessionID != "" {
		parts = append(parts, "session="+turn.SessionID)
	}
	parts = append(parts, fmt.Sprintf("step=%d/%s", turn.Step, formatChatDebugTurnMaxSteps(turn.MaxSteps)))
	if turn.FinishedAt.IsZero() {
		parts = append(parts, "state=running")
	} else {
		parts = append(parts, "elapsed="+chatDebugTurnDuration(turn.ElapsedMS))
	}
	if turn.BudgetLevel != "" {
		parts = append(parts, "level="+turn.BudgetLevel)
	}
	if turn.BudgetRatio > 0 {
		parts = append(parts, fmt.Sprintf("ratio=%.2f", turn.BudgetRatio))
	}
	return strings.Join(parts, " ")
}

// formatChatDebugTurnMaxSteps 把未配置步数上限（0）渲染为 "unlimited"，
// 与 agent 侧 NormalizeMaxSteps 的语义一致。
func formatChatDebugTurnMaxSteps(maxSteps int) string {
	if maxSteps <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(maxSteps)
}

// chatDebugTurnDuration 以秒为粒度渲染耗时：debug 区块用于判断"这轮跑了多久"，
// 不需要毫秒精度。
func chatDebugTurnDuration(elapsedMS int64) string {
	if elapsedMS <= 0 {
		return "0s"
	}
	return time.Duration(elapsedMS * int64(time.Millisecond)).Round(time.Second).String()
}
