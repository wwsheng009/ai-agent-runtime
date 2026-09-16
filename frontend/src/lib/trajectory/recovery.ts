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
import {
  CHAT_SSE_EVENT_PREFIX,
  RUNTIME_EVENT_CHAT_BRIDGE_TYPES,
  RUNTIME_EVENT_PERSISTED_TYPES,
  RUNTIME_EVENT_PROVENANCE_TYPES,
} from "@/types/runtime/event-contract";

import { TRAJECTORY_ITEM_ID_KEY, type TrajectoryEventKind } from "./types";
import { subagentItemId } from "./entity-identity";

// 帧名前缀来自事件契约生成物（与后端 api/skills 的常量同源）；此处再导出以保持
// 既有 import 面（export.ts 等模块从本模块取用）。
export { CHAT_SSE_EVENT_PREFIX };

export const TRAJECTORY_RECOVERY_PAGE_SIZE = 500;

/**
 * 尾部优先回放（tail-first）的首屏窗口：只回放最近 N 条事件。
 *
 * 大会话实测（2288 条事件 / 2.25MB SSE dump）：首屏从 seq=0 全量重放是
 * long task 与卡顿的主要来源，而用户打开会话时关心的是**最近的消息**。
 * 因此首屏只取最后一页，旧内容由「加载更早」按需逐页向前（见
 * `TRAJECTORY_EARLIER_PAGE_EVENTS`），窗口之前的 seq 用基准游标跳过
 * （`TrajectoryStore.setBaselineSeq`）。
 */
export const TRAJECTORY_TAIL_WINDOW_EVENTS = 800;

/** 「加载更早」每次向前取的事件条数（与恢复分页同量级）。 */
export const TRAJECTORY_EARLIER_PAGE_EVENTS = TRAJECTORY_RECOVERY_PAGE_SIZE;

/**
 * 尾部窗口落地后的补齐次数上限（每轮一页）。
 *
 * 窗口回放依赖「事件序列连续推进游标」，但游标可能停在窗口末尾之前：同一段
 * 增量已被实时路径应用（`seenDeltaKeys` 去重后不再推进）、后端返回的一页被
 * 截断、或 EventStore 保留期丢弃了中间的持久化事件。此时窗口渲染不全，窗口
 * 落地后按 `after` 续拉补齐；游标已到窗口末尾则一次请求都不发（正常路径零成本）。
 */
export const TRAJECTORY_WINDOW_REPAIR_MAX_PAGES = 8;

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

/**
 * Assistant streaming events are emitted directly by the ReAct runtime rather
 * than through the HTTP chat SSE envelope.  They still belong to the same
 * durable trajectory and must be projected through the regular chunk /
 * reasoning reducer paths during recovery.
 *
 * Batch 2：名单由事件契约生成物派生——落盘的 `assistant*` 事件加上契约未登记
 * 的旧别名（历史行仍可能出现）。新增助手事件只要登记进
 * `internal/events/contract.go` 就会自动纳入；text/reasoning/image 的语义分类由
 * `thread-state/deltas.getRuntimeDeltaKind` 承担，两层覆盖由
 * `recovery-contract.test.ts` 门禁。
 */
const LEGACY_ASSISTANT_RUNTIME_EVENT_TYPES = [
  // runtimeobserve 记录过、但未登记进交付契约的旧拼写。
  "assistant.reasoning_delta",
] as const;

export const ASSISTANT_RUNTIME_EVENT_TYPES: ReadonlySet<string> = new Set([
  ...RUNTIME_EVENT_PERSISTED_TYPES.filter((type) =>
    type.startsWith("assistant"),
  ),
  ...LEGACY_ASSISTANT_RUNTIME_EVENT_TYPES,
]);

export type TrajectoryRecoveryPush = {
  kind: TrajectoryEventKind;
  payload: Record<string, unknown>;
};

/**
 * 可映射进轨迹的 runtime 生命周期事件白名单（Q4）。
 *
 * Batch 2：不再手写清单，由事件契约生成物派生——落盘（session_store）事件减去
 * 助手增量（专门路径）、工具帧桥（chat_bridge；工具生命周期已由
 * chat.sse.tool_start/tool_end 呈现，避免重复行）与 provenance 承载类型（不建行）。
 * 注册表新增落盘生命周期事件会自动进入本集合，经 `runtimeEventToTrajectoryPush`
 * 的通用 runtime 行渲染——把类型漂移从「静默丢弃」升级为可见行（P0-1）。
 */
export const RUNTIME_EVENT_TYPES: ReadonlySet<string> = new Set([
  ...RUNTIME_EVENT_PERSISTED_TYPES.filter(
    (type) =>
      !ASSISTANT_RUNTIME_EVENT_TYPES.has(type) &&
      !RUNTIME_EVENT_CHAT_BRIDGE_TYPES.includes(type) &&
      !RUNTIME_EVENT_PROVENANCE_TYPES.includes(type),
  ),
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

/**
 * 工具观测帧判定：`chat.sse.observation` 是否只是**已有工具行的重复来源**。
 *
 * 后端 `buildObservationEventPayloads`（handler.go:8089）**只**从
 * `resultPayload["observations"]`（ReAct 工具观测）构造该帧，字段是 `step` /
 * `tool`（工具**名**，不是 provider call id）/ `input` / `output` / `success` /
 * `error` / `duration_ms` / `metrics`。没有 call id ⇒ 轨迹侧无法把它折叠进
 * `tool:<call_id>` 行，只能另起 `observation-<seq>`。
 *
 * 但同一批次的 `buildObservedToolEventPayloads*`（handler.go:1888 / 7832）已经把
 * **同一份**观测内容发成 `chat.sse.tool_end`（带 provider call id），前端按
 * `tool:<call_id>` upsert 出结构完整的工具行（名称 + 入参 + 输出 + 耗时）。
 * 两路各自建行 ⇒ 同一条命令在轨迹里出现两次。
 *
 * 实测（会话 `session_20260915211037_9e9CQmZq`，单轮 953 帧）：轨迹行序为
 * `tool:call_00` / `tool:call_01` → 正文消息 → `observation-951` /
 * `observation-952`——同一对 shell 工具在消息之后又各多出一行（载荷里是同一
 * 条 command/input/output）。聊天链路早已把该帧当「阶段推进信号」处理
 * （`thread-state/deltas.ts` 的 `getRuntimeBridgeKind` 返回 phase，不建行）。
 *
 * 轨迹链路与之对齐：本判定为真时 `chatSseEventToTrajectoryPush` 返回 null，
 * `trajectoryEventAction` 落到 `skip(seq)`——推进持久化游标（避免后续事件永久
 * 卡 pending）、不产生行。判据是「是否携带工具身份」而不是「kind 是否等于
 * observation」：无工具身份的观测帧（G7 结构化事件）语义不变，仍按行渲染。
 */
function isToolObservation(event: SessionRuntimeEvent): boolean {
  if (event.type !== `${CHAT_SSE_EVENT_PREFIX}observation`) {
    return false;
  }
  const payload = event.payload ?? {};
  return (
    readTrimmedString(payload["tool"]) !== undefined ||
    readTrimmedString(payload["step"]) !== undefined
  );
}

/**
 * 把一条 chat SSE 事件转为轨迹 push；非 chat SSE 事件返回 null。
 *
 * P0-2（批次 20）：这里原有一份 `KNOWN_TRAJECTORY_KINDS` 前置白名单，未命中的
 * kind 直接 `return null`。但本函数是恢复链路唯一的入闸处，白名单因此把
 * `applySequencedEvent` 的 `unknown-<seq>` 兜底分支变成了死代码——后端新增一个
 * emitter 名字时，前端只是「没有反应」，与「本来就没有这条事件」不可区分。
 *
 * 现在改为放行：未知 kind 进入 reducer 兜底，生成可见的降级 system 行
 * （`apply.ts` 默认分支），把类型漂移从静默丢弃升级为可观测事件。chat 路径
 * 当前发出的名字全部落在 reducer 已映射集合内，因此本改动不新增任何行；
 * 它只在「后端新增 emitter 名字而前端未同步」时生效。已知 kind 归约语义不变。
 */
export function chatSseEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  if (!isChatSseEvent(event)) {
    return null;
  }
  // 工具观测帧不建行（与聊天链路同口径，见 isToolObservation）。
  if (isToolObservation(event)) {
    return null;
  }
  const kind = event.type.slice(CHAT_SSE_EVENT_PREFIX.length) as TrajectoryEventKind;
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
 * live-only 事件无持久化 seq → `_event.sequence=0`，因此身份必须显式给出：
 * `_trajectory_item_id = subagent:<child>`（见 entity-identity.ts）。每个子代理
 * 一行、同一子代理的后续镜像就地更新（服务端已按窗口节流 + 状态变化穿透，不会
 * 产生事件风暴行）。P1-1（批次 20）修复前所有子代理共用 `runtime-0` 而互相覆盖。
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
    [TRAJECTORY_ITEM_ID_KEY]: subagentItemId(childId),
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
