/**
 * Trajectory reducer（纯函数）
 *
 * 对齐 TUI `encoder.go` / `render-model-spec.md` §4/§6 语义：
 * - 提交语义：append / upsert / remove，幂等规则见 spec §4.2；
 * - 状态机：pending → running → completed / failed / canceled；终态后仅允许 remove；
 * - 乱序免疫：事件带持久化 seq，未按序事件先缓冲，前序补齐后按序应用；
 * - upsert 退化规则：按 ID 找不到时退化为 append（输出先于调用到达时自成一个块）。
 *
 * 不变式：同一事件序列重放 → 相同快照（ID/Seq 由事件内容确定性派生）。
 */

export { makeTrajectoryEvent, describeRuntimeEvent, eventSeqOf, textDeltaOf, toolCallIdOf, toolNameOf } from "./event-readers";
export { TERMINAL_STATUSES } from "./snapshot-ops";
export { removeItem, applyEvent, advanceSeqCursor, applyEvents } from "./api";
