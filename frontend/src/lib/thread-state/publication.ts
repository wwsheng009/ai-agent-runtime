/**
 * P1-2 发布分级（publication grading）：为流事件声明"是否上屏、以什么节奏上屏"。
 *
 * 参考取证（deepseek-harness，只读）：
 * - `ui-conversation/.../conversation/assembly.ts:130-147`：`none` 直接不发布；
 *   `animation-frame` 连跨三次绘制机会后发布（三帧门，60fps 下 ≈20Hz 上限）；
 *   `immediate` 取消挂起帧立即发布。
 * - `ui-chat/.../conversation-nodes/assistant.ts:321-325`：`step/start` → none；
 *   非 `assistant/live-chunk` → immediate；live-chunk 的 `usage`/`finish` → none，
 *   其余正文/推理 → animation-frame。
 * - `turn-process.ts:240-245`：live-chunk 的 usage/finish → none，其余事件 → immediate。
 * - `inbox.ts:125`、`turn-tail.ts:186`：镜像/维护类 → none，回合结算 → immediate。
 *
 * 本仓库事件模型没有 `chunk.type ∈ {usage, finish}` 子类型（usage 走 analytics REST，
 * 不在事件流里），因此 **none 档当前无落点**（类型保留、映射为空集）：真正"高频
 * 低价值"的 live-only 节流镜像（P1-5：`tool/subagent.progress`，后端节流后随
 * `live=1` 投递、不落库、无持久 seq）有 UI 落点（工具行 / 子代理进度条），归
 * `animation-frame` 档让三帧门吸收其频率；若未来事件契约引入 usage/finish 类事件，
 * 在此落地 none 语义（对齐参考 assistant.ts:325）。
 *
 * 不变式：
 * - **分级只决定"是否触发提交、用什么节奏提交"，不决定事件是否入队**——所有事件
 *   都进 pendingRuntimeCommits（线程状态必须推进，快照不得陈旧），none 只是不主动
 *   调度，由后续 animation-frame / immediate 提交顺带兑现（批合并，与"一条一次
 *   提交"顺序应用语义一致）。
 * - 分级判定是纯函数（单测可覆盖）；事件类型名单变更是契约漂移，测试门禁在
 *   `types/runtime/event-contract.test.ts` 同层。
 */
import { type SessionRuntimeEvent } from "@/types/runtime";

import { getRuntimeDeltaKind } from "./deltas";

export type PublicationLevel = "none" | "animation-frame" | "immediate";

/**
 * live-only 节流镜像事件（P1-5：`live=1` 时由后端节流后随 SSE 投递、不落库、无
 * 持久 seq）：驱动工具行 / 子代理进度条，有 UI 落点；归 `animation-frame` 档让
 * 帧门吸收其后端节流频率，避免"纯进度流不提交"的冻结。
 */
const FRAME_PACED_MIRROR_EVENT_TYPES: ReadonlySet<string> = new Set([
  "tool.progress",
  "subagent.progress",
  "subagent.batch.progress",
]);

/**
 * 事件 → 发布级别。
 *
 * - delta 家族（`getRuntimeDeltaKind(type) !== null`，正文 / 推理 / 图片增量）→
 *   `animation-frame`：120ms 下界 + 三帧门节奏上屏（≈20Hz 上限），对齐参考的正文
 *   live-chunk；live-only 节流镜像（tool/subagent.progress）同档——有 UI 落点，
 *   帧门吸收其后端节流频率，避免"纯进度流不提交"的冻结；
 * - 其余持久事件（结算、工具生命周期、回滚、approval、上下文、回合态…）→
 *   `immediate`：取消挂起帧立即提交，对齐参考的"非 live-chunk → immediate"。
 */
export function getPublicationLevel(event: SessionRuntimeEvent): PublicationLevel {
  if (
    getRuntimeDeltaKind(event.type) !== null ||
    FRAME_PACED_MIRROR_EVENT_TYPES.has(event.type)
  ) {
    return "animation-frame";
  }
  return "immediate";
}
