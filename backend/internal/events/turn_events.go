package events

// 托管 turn（§6.12 parked turn）的可观测事件名常量（方案 §6.8 / 审计缺口 G3）。
//
// 背景：挂起态此前**没有任何对外事件**——`turn.suspended` 在生产零发射点，
// 而回合收尾无条件发 `agent.turn.finished`，于是 UI/分析面把"托管挂起"看成
// "回合已结束"，用户无法区分"挂起等待"与"卡死"。本文件把两个里程碑事件名
// 定义一次，发射点只引用常量（与 subagent_audit_events.go / main_agent_routing.go
// 同构）：
//
//  1. `turn.suspended`：durable 宿主**首次**为某个 turn 写入挂起记录后发射
//     （internal/agent/loop.go 的 parkBackgroundTurn）。降级宿主（无 durable
//     store / 进程内 store）永不发射——与 AC-C0-1d"降级路径不出现 turn.suspended"
//     一致；同一 turn 派发多个批次也只发一次（边沿触发）。
//  2. `turn.resumed`：宿主把 wake 真正投递成一次 resume episode 之后发射
//     （internal/supervision 的 WakeConsumer.Announce，由 CLI/API 宿主接到各自
//     的事件总线上）。投递失败（wake 重新排队）不发——否则 UI 会显示一个从未
//     发生的恢复。
//
// 语义边界（审计 G3 的另一半）：`agent.turn.finished` 表示**本次 run 结束**，
// 不等于托管 turn 结束。挂起期的 run 收尾仍然会发 `agent.turn.finished`（这是
// 运行预算/耗时统计的事实），托管状态由 turn.suspended / turn.resumed 表达；
// 状态侧另见 RuntimeStateSummary.SuspendedTurnID（durable 派生，重启后一致）。
const (
	// EventTurnSuspended：托管 turn 进入挂起态。载荷（宿主事件总线）：
	// turn_id、session_id、batch_id、obligation_count、parked_at、resume_queue_count。
	EventTurnSuspended = "turn.suspended"

	// EventTurnResumed：宿主开始一次 resume episode。载荷（宿主事件总线）：
	// turn_id、session_id、trigger（terminal/progress/approval/other）、
	// wake_reasons、wake_ids、pending_count、status、terminal、summary。
	//
	// 闭合半边（2026-09-26 真机 E2E 补）：trigger=TurnResumedTriggerSettled 时
	// 表示挂起态在**同一 run 内**被结清（义务已全部终态），不是 wake 投递的
	// resume episode；载荷见 settleParkedTurnOnRunEnd。
	EventTurnResumed = "turn.resumed"

	// TurnResumedTriggerSettled 是 turn.resumed 的非 wake 闭合触发值：run 结束时
	// 挂起记录的全部义务已终态，记录被结清——没有、也不会有 resume episode。
	//
	// 背景（真机 E2E 复现）：结清路径原先静默清记录，只有 turn.suspended 播报出去，
	// 而前端契约是"只有 turn.resumed 清除挂起态"（frontend/src/lib/parked-turn/
	// events.ts），于是"托管中"横幅在同一 run 结清的场景下永久卡住。
	TurnResumedTriggerSettled = "settled"
)
