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

/** live-only 工具进度事件类型（对齐后端 `toolprotocol.EventTypeProgress`）。 */
export const TOOL_PROGRESS_EVENT_TYPE = "tool.progress";

/**
 * live-only 子会话进度镜像事件类型（P1-5 方案 2）。
 *
 * 对齐后端 `supervision.EventTypeSubagentProgress`：父会话订阅子 agent 事件时
 * 把子会话的 `tool.progress` 按窗口节流后镜像到父流。事件不落库（父/子
 * EventStore 均无该行），只经 `runtime/stream?live=1` 送达前端。
 */
export const SUBAGENT_PROGRESS_EVENT_TYPE = "subagent.progress";

function readTrimmedString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

function readFiniteNumber(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return undefined;
}

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

/**
 * Assistant streaming events are emitted directly by the ReAct runtime rather
 * than through the HTTP chat SSE envelope.  They still belong to the same
 * durable trajectory and must be projected through the regular chunk /
 * reasoning reducer paths during recovery.
 */
export const ASSISTANT_RUNTIME_EVENT_TYPES: ReadonlySet<string> = new Set([
  "assistant_delta",
  "assistant_reasoning",
  "assistant.reasoning",
  "assistant.reasoning_delta",
  "assistant.image_progress",
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
  // P2-8 方案 4：配额驱逐（spawn 闸门 / `/agents cleanup`）的产品事件，
  // 由父会话流渲染为一行 system note，解释“子会话为何消失、是谁回收的”。
  "agent.reclaimed",
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

export function isAssistantRuntimeEvent(event: SessionRuntimeEvent): boolean {
  return ASSISTANT_RUNTIME_EVENT_TYPES.has(event.type);
}

/**
 * 是否为「内容帧」：轨迹消息行（user/assistant/tool/reasoning）的正常来源，
 * 即 `chat.sse.*` 与 assistant reporter 事件。
 *
 * 恢复链路用它判定会话是否真的有可渲染内容：一条内容帧都没有的会话
 * （例如由 aicli 进程内 chat 运行时执行、只落生命周期事件的会话）必须回退到
 * 会话历史投影（P4，见 session-history.ts），否则轨迹只剩 system 行。
 */
export function isTrajectoryContentEvent(event: SessionRuntimeEvent): boolean {
  return isChatSseEvent(event) || isAssistantRuntimeEvent(event);
}

/** 读取事件持久化 seq（后端 ListEvents 注入 payload.seq）。 */
export function chatSseEventSeq(event: SessionRuntimeEvent): number {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq) && rawSeq > 0) {
    return Math.floor(rawSeq);
  }
  if (typeof rawSeq === "string") {
    const parsed = Number(rawSeq.trim());
    if (Number.isFinite(parsed) && parsed > 0) {
      return Math.floor(parsed);
    }
  }
  return 0;
}

/**
 * 恢复帧的 `_event` envelope：与后端 SSE 帧同形（sequence + timestamp）。
 *
 * `timestamp` 是轨迹时间线的墙钟时间来源（live 帧由后端 `wrapSSEData` 写入，
 * 恢复帧由 EventStore 的 `SessionRuntimeEvent.timestamp` 回填），缺失时时间线
 * 自动退化为序号轴。
 */
function eventEnvelope(
  seq: number,
  timestamp?: string,
): Record<string, unknown> {
  const envelope: Record<string, unknown> = { sequence: seq };
  const text = typeof timestamp === "string" ? timestamp.trim() : "";
  if (text) {
    envelope.timestamp = text;
  }
  return envelope;
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
  payload._event = eventEnvelope(seq, event.timestamp);
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
  payload._event = eventEnvelope(seq, event.timestamp);
  return { kind: "runtime", payload };
}

/**
 * Convert the durable assistant reporter protocol into the equivalent
 * trajectory event shape.  Keeping this conversion here (instead of teaching
 * every consumer about two payload dialects) makes live delivery and recovery
 * deterministic.
 */
export function assistantRuntimeEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (!isAssistantRuntimeEvent(event)) {
    return null;
  }

  const source = { ...(event.payload ?? {}) };
  const seq = chatSseEventSeq(event);
  delete source.seq;
  source._event = eventEnvelope(seq, event.timestamp);

  if (event.type === "assistant_delta") {
    source.type = "text";
    if (
      typeof source.content !== "string" &&
      typeof source.delta === "string"
    ) {
      source.content = source.delta;
    }
    return { kind: "chunk", payload: source };
  }

  if (
    event.type === "assistant_reasoning" ||
    event.type === "assistant.reasoning" ||
    event.type === "assistant.reasoning_delta"
  ) {
    return { kind: "reasoning", payload: source };
  }

  // The runtime image reporter nests provider metadata under `image`, while
  // the chat SSE chunk contract uses `metadata`.
  source.type = "image";
  if (
    source.metadata === undefined &&
    source.image &&
    typeof source.image === "object"
  ) {
    source.metadata = source.image;
  }
  return { kind: "chunk", payload: source };
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

/**
 * live-only `tool.progress` → 轨迹 push（P1-5 子会话下钻）。
 *
 * 语义：作为**既有工具行的进行中更新**（kind="tool_call"，会与
 * `chat.sse.tool_start`/`tool_end` 折叠为同一 Item `tool:<call_id>`），
 * 而不是新增一行——进度是高频事件，逐条建行会淹没轨迹。
 * - 文本取 `partial`（流式输出）优先，回退 `message`；
 * - `percent` 原样带给 reducer（UI 可展示完成度）；
 * - 无 `tool_call_id` 时无法折叠到既有行 → ignore，避免产生无主行；
 * - live-only 事件不落库、无持久化 seq → `_event.sequence=0`，
 *   reducer 按到达顺序即时应用（不参与 seq 空洞判定）。
 */
export function toolProgressEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (event.type !== TOOL_PROGRESS_EVENT_TYPE) {
    return null;
  }
  const source = event.payload ?? {};
  const callId = readTrimmedString(source.tool_call_id);
  if (!callId) {
    return null;
  }
  const name =
    readTrimmedString(event.tool_name) ??
    readTrimmedString(source.tool_name) ??
    "tool";
  const text =
    readTrimmedString(source.partial) ?? readTrimmedString(source.message);
  const percent = readFiniteNumber(source.percent);

  const payload: Record<string, unknown> = {
    type: "tool_call",
    tool_call: { id: callId, name },
    live: true,
    _event: { sequence: 0 },
  };
  const tool: Record<string, unknown> = {};
  if (text) {
    tool.output_summary = text;
  }
  if (percent !== undefined) {
    tool.percent = percent;
  }
  if (Object.keys(tool).length > 0) {
    payload.tool = tool;
  }
  return { kind: "tool_call", payload };
}

/**
 * P1-5 方案 2：父流节流镜像 `subagent.progress` → 轨迹折叠行。
 *
 * 契约（对齐后端 `supervision.SubagentProgressMirror.Observe`）：
 * - `agent_id` / `session_id`：子会话 ID（缺一即可，两者皆缺则 ignore）；
 * - `parent_session_id`：父会话 ID；`path`：子 agent 路径（可选）；
 * - `state`：`running` / `completed` / `failed` 等工具状态；
 * - `tool_name` / `tool_call_id` / `partial` / `message` / `percent`：工具进度；
 * - `live: true` 标注镜像行（非父会话自身的工具调用）。
 *
 * live-only 事件无持久化 seq → `_event.sequence=0`：同一父流的所有镜像共用
 * `runtime-0` 一项（reducer 按 ID upsert），父轨迹只保留最新一条折叠行——
 * 与服务端「窗口内合并、状态变化穿透」的节流语义一致，不产生事件风暴行。
 */
export function subagentProgressEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (event.type !== SUBAGENT_PROGRESS_EVENT_TYPE) {
    return null;
  }
  const source = event.payload ?? {};
  const childId =
    readTrimmedString(source.agent_id) ?? readTrimmedString(source.session_id);
  if (!childId) {
    return null;
  }
  const path =
    readTrimmedString(source.path) ?? readTrimmedString(source.agent_path);

  const payload: Record<string, unknown> = {
    runtime_type: SUBAGENT_PROGRESS_EVENT_TYPE,
    ...source,
    agent_id: childId,
    live: true,
    _event: { sequence: 0 },
  };
  if (path) {
    payload.agent_path = path;
  }
  delete payload.seq;
  return { kind: "runtime", payload };
}

export function trajectoryEventAction(
  event: SessionRuntimeEvent,
): TrajectoryEventAction {
  const subagentPush = subagentProgressEventToTrajectoryPush(event);
  if (subagentPush) {
    return { kind: "push", push: subagentPush };
  }
  const progressPush = toolProgressEventToTrajectoryPush(event);
  if (progressPush) {
    return { kind: "push", push: progressPush };
  }
  const chatPush = chatSseEventToTrajectoryPush(event);
  if (chatPush) {
    return { kind: "push", push: chatPush };
  }
  const assistantPush = assistantRuntimeEventToTrajectoryPush(event);
  if (assistantPush) {
    return { kind: "push", push: assistantPush };
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
      chatSseEventToTrajectoryPush(event) ??
      assistantRuntimeEventToTrajectoryPush(event) ??
      runtimeEventToTrajectoryPush(event);
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
