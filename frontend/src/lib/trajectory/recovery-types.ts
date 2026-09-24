// P0-2 拆分（原 recovery.ts L103-L106、L323-L334）：恢复链路的公共类型。
// 拆出原因：类型与实现分离，供 recovery.ts / recovery-runtime-tools.ts 共用。

import type { TrajectoryEventKind } from "./types";

export type TrajectoryRecoveryPush = {
  kind: TrajectoryEventKind;
  payload: Record<string, unknown>;
};

/**
 * 单条事件的轨迹动作（恢复与轮询共用）：
 * - push：可渲染事件（chat.sse 或白名单 runtime 生命周期），由调用方推入 reducer；
 * - skip：被过滤但已持久化的事件（tool_started/tool_finished/context.profile.
 *   injected/recall.performed 等，与 chat.sse 共享同一 EventStore 全局 seq）——
 *   记录其 seq，调用方 advanceCursor 跳过空洞，避免后续事件永久卡 pending；
 * - ignore：无持久化 seq 的暂态事件，无需处理。
 */
export type TrajectoryEventAction =
  | { kind: "push"; push: TrajectoryRecoveryPush }
  | { kind: "skip"; seq: number }
  | { kind: "ignore" };
