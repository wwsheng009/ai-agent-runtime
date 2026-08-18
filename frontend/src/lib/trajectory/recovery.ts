/**
 * 轨迹断线恢复（P3-1）：EventStore 事件 → 轨迹 reducer 重放。
 *
 * 契约（对齐后端 P0）：
 * - SSE 实时帧：`data: { ..., _event: { sequence } }`（前端实时流读取）；
 * - runtime/events 增量拉取：`{ type: "chat.sse.<kind>", payload: { ..., seq } }`
 *   （后端 ListEvents 在返回前把持久化 seq 注入 `payload.seq`）。
 * 本模块把拉取事件转回实时流形态（`_event.sequence`），复用同一
 * reducer 重放路径；`payload.seq` 为游标字段，不进入轨迹事件载荷。
 */
import type { SessionRuntimeEvent } from "@/types/runtime";

import type { TrajectoryEventKind } from "./types";

export const CHAT_SSE_EVENT_PREFIX = "chat.sse.";

export const TRAJECTORY_RECOVERY_PAGE_SIZE = 500;

/** 已知轨迹事件 kind（未知/未来事件跳过，保持 reducer 边界严格）。 */
const KNOWN_TRAJECTORY_KINDS = new Set<TrajectoryEventKind>([
  "meta",
  "chunk",
  "reasoning",
  "tool_start",
  "tool_call",
  "tool_end",
  "planning",
  "orchestration",
  "route",
  "observation",
  "subagent",
  "result",
  "done",
  "error",
  "runtime",
]);

export type TrajectoryRecoveryPush = {
  kind: TrajectoryEventKind;
  payload: Record<string, unknown>;
};

/**
 * 可映射进轨迹的 runtime 生命周期事件白名单（Q4；对齐后端
 * `shouldPersistRuntimeSessionEvent` 扩展集，排除 tool_started/tool_finished——
 * 工具生命周期已由 chat.sse.tool_start/tool_end 呈现，避免重复行）。
 */
export const RUNTIME_EVENT_TYPES: ReadonlySet<string> = new Set([
  "approval_requested",
  "approval_resolved",
  "session_compact_started",
  "session_compact_completed",
  "session_compact_skipped",
  "session_compact_failed",
  "session_start",
  "session_end",
  "session_interrupted",
  "context_reconciled",
  "checkpoint_created",
]);

/** 事件是否为 chat SSE 轨迹事件（可重放进轨迹 reducer）。 */
export function isChatSseEvent(event: SessionRuntimeEvent): boolean {
  return event.type.startsWith(CHAT_SSE_EVENT_PREFIX);
}

/** 事件是否为可映射的 runtime 生命周期事件（Q4 白名单）。 */
export function isRuntimeTrajectoryEvent(event: SessionRuntimeEvent): boolean {
  return RUNTIME_EVENT_TYPES.has(event.type);
}

/** 读取事件持久化 seq（后端 ListEvents 注入 payload.seq）。 */
export function chatSseEventSeq(event: SessionRuntimeEvent): number {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq) && rawSeq > 0) {
    return Math.floor(rawSeq);
  }
  return 0;
}

/** 把一条 chat SSE 事件转为轨迹 push；非轨迹/未知 kind 返回 null。 */
export function chatSseEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (!isChatSseEvent(event)) {
    return null;
  }
  const kind = event.type.slice(CHAT_SSE_EVENT_PREFIX.length) as TrajectoryEventKind;
  if (!KNOWN_TRAJECTORY_KINDS.has(kind)) {
    return null;
  }
  const payload: Record<string, unknown> = { ...(event.payload ?? {}) };
  const seq = chatSseEventSeq(event);
  delete payload.seq;
  payload._event = { sequence: seq };
  return { kind, payload };
}

/**
 * runtime 生命周期事件 → 轨迹 push（Q4）：kind="runtime"，payload 保留
 * 原始 type（runtime_type）与字段，注入 _event.sequence 供 reducer 排序/幂等。
 */
export function runtimeEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (!isRuntimeTrajectoryEvent(event)) {
    return null;
  }
  const payload: Record<string, unknown> = {
    runtime_type: event.type,
    ...(event.payload ?? {}),
  };
  const seq = chatSseEventSeq(event);
  delete payload.seq;
  payload._event = { sequence: seq };
  return { kind: "runtime", payload };
}

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

export function trajectoryEventAction(
  event: SessionRuntimeEvent,
): TrajectoryEventAction {
  const chatPush = chatSseEventToTrajectoryPush(event);
  if (chatPush) {
    return { kind: "push", push: chatPush };
  }
  const runtimePush = runtimeEventToTrajectoryPush(event);
  if (runtimePush) {
    return { kind: "push", push: runtimePush };
  }
  const seq = chatSseEventSeq(event);
  if (seq > 0) {
    return { kind: "skip", seq };
  }
  return { kind: "ignore" };
}

/** 分页拉取的下一游标（最后一事件的持久化 seq；无事件时保持原游标）。 */
export function nextRecoveryAfter(
  events: SessionRuntimeEvent[],
  fallbackAfter: number,
): number {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const seq = chatSseEventSeq(events[index]);
    if (seq > 0) {
      return seq;
    }
  }
  return fallbackAfter;
}

/** 全部事件转为有序轨迹 push 序列（按 seq 升序，seq=0 事件排末尾）。 */
export function trajectoryRecoveryPushes(
  events: SessionRuntimeEvent[],
): TrajectoryRecoveryPush[] {
  const pushes: TrajectoryRecoveryPush[] = [];
  const zeroSeq: TrajectoryRecoveryPush[] = [];
  for (const event of events) {
    const push =
      chatSseEventToTrajectoryPush(event) ?? runtimeEventToTrajectoryPush(event);
    if (!push) {
      continue;
    }
    const seq = (push.payload._event as { sequence: number }).sequence;
    if (seq > 0) {
      pushes.push(push);
    } else {
      zeroSeq.push(push);
    }
  }
  pushes.sort((left, right) => {
    const leftSeq = (left.payload._event as { sequence: number }).sequence;
    const rightSeq = (right.payload._event as { sequence: number }).sequence;
    return leftSeq - rightSeq;
  });
  return [...pushes, ...zeroSeq];
}
