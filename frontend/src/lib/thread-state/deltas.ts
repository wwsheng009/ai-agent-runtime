// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type SessionRuntimeEvent } from "@/types/runtime";
import {
  CHAT_SSE_EVENT_PREFIX,
  RUNTIME_EVENT_PERSISTED_TYPES,
} from "@/types/runtime/event-contract";

export type RuntimeDeltaKind = "text" | "reasoning" | "image";

/**
 * Coordinates the two live delivery paths used by the workspace:
 * `/api/agent/chat` SSE and the session runtime stream. Both paths carry the
 * same provider delta, so claiming its stable stream identity before applying
 * it makes delivery order irrelevant.
 */
export type RuntimeDeltaCoordinator = {
  beginTurn: (turnId: string) => void;
  endTurn: (turnId?: string) => void;
  isTurnActive: (turnId?: string) => boolean;
  claim: (key: string) => boolean;
};

const MAX_RUNTIME_DELTA_KEYS = 512;

export function createRuntimeDeltaCoordinator(): RuntimeDeltaCoordinator {
  const seenKeys = new Set<string>();
  let activeTurnId = "";

  return {
    beginTurn(turnId: string) {
      activeTurnId = turnId.trim();
      seenKeys.clear();
    },
    endTurn(turnId?: string) {
      const normalized = turnId?.trim() ?? "";
      if (!normalized || normalized === activeTurnId) {
        activeTurnId = "";
      }
    },
    isTurnActive(turnId?: string) {
      if (!activeTurnId) {
        return false;
      }
      const normalized = turnId?.trim() ?? "";
      return !normalized || normalized === activeTurnId;
    },
    claim(key: string) {
      const normalized = key.trim();
      if (!normalized) {
        // Legacy providers may not expose identity. Callers can still
        // suppress those payloads while a direct request stream is active.
        return true;
      }
      if (seenKeys.has(normalized)) {
        return false;
      }
      seenKeys.add(normalized);
      while (seenKeys.size > MAX_RUNTIME_DELTA_KEYS) {
        const oldest = seenKeys.values().next().value as string | undefined;
        if (oldest === undefined) {
          break;
        }
        seenKeys.delete(oldest);
      }
      return true;
    },
  };
}

function readDeltaIdentityValue(
  payload: Record<string, unknown> | undefined,
  keys: string[],
): string {
  if (!payload) {
    return "";
  }
  for (const key of keys) {
    const value = payload[key];
    if (
      (typeof value === "string" || typeof value === "number") &&
      String(value).trim()
    ) {
      return String(value).trim();
    }
  }
  return "";
}

/**
 * Return the cross-channel identity for one text/reasoning/image delta.
 * EventStore `_event.sequence` is deliberately not used: it is assigned by
 * each transport independently, while provider stream_id + sequence is shared.
 */
export function getRuntimeDeltaKey(
  payload: Record<string, unknown> | undefined,
  kind: RuntimeDeltaKind,
): string {
  const streamId = readDeltaIdentityValue(payload, ["stream_id", "streamId"]);
  const sequence = readDeltaIdentityValue(payload, [
    "sequence",
    "stream_sequence",
    "streamSequence",
  ]);
  if (!streamId || !sequence) {
    return "";
  }
  const turnId = readDeltaIdentityValue(payload, ["turn_id", "turnId", "turn"]);
  return ["runtime-delta", turnId, streamId, kind, sequence].join("|");
}

/**
 * 助手增量家族（Batch 2：名单由事件契约生成物派生）。
 *
 * 成员 = 契约里落盘的 `assistant*`（`internal/events/contract.go` 注册表经
 * codegen 投影到 `@/types/runtime/event-contract`）+ 总线历史别名：
 * - `assistant.delta` 是 `assistant_delta` 的点形态（runtimeobserve/known_types.go
 *   明确记录两形态都在用），通道为空但历史总线在用；
 * - `assistant.reasoning_delta` 是推理增量的旧拼写，未登记进契约。
 * 漏归类会让打字机增量整条静默丢弃（既不入消息也不占去重键），因此别名集中
 * 在这里、由注释解释，而不是散落回 switch。
 *
 * 语义分类走命名约定：`.image_progress` → image；含 `reasoning` → reasoning；
 * 其余 → text。新增落盘 `assistant*` 类型会自动纳入（漂移不再表现为静默丢弃），
 * 分类覆盖由 `types/runtime/event-contract.test.ts` 门禁。
 */
const LEGACY_ASSISTANT_DELTA_ALIASES = [
  "assistant.delta",
  "assistant.reasoning_delta",
] as const;

const ASSISTANT_DELTA_TYPES: ReadonlySet<string> = new Set([
  ...RUNTIME_EVENT_PERSISTED_TYPES.filter((type) =>
    type.startsWith("assistant"),
  ),
  ...LEGACY_ASSISTANT_DELTA_ALIASES,
]);

export function getRuntimeDeltaKind(
  eventType: string,
): RuntimeDeltaKind | null {
  if (!ASSISTANT_DELTA_TYPES.has(eventType)) {
    return null;
  }
  if (eventType.endsWith(".image_progress")) {
    return "image";
  }
  if (eventType.includes("reasoning")) {
    return "reasoning";
  }
  return "text";
}

/**
 * 是否为图片生成增量事件。历史快照路径（`thread-state/events.ts`）据此在回放
 * 中途恢复的会话里补图片占位段——与 `assistant.image_progress` 的旧字面量判定
 * 等价，但种类来自同一处契约派生。
 */
export function isAssistantImageProgressEvent(eventType: string): boolean {
  return getRuntimeDeltaKind(eventType) === "image";
}

/** 工具行的三态：与 `ToolMessageSegment["status"]` 的取值一一对应。 */
export type RuntimeBridgeToolStatus = "started" | "running" | "finished";

/**
 * `chat.sse.*` 帧在 runtime/stream 通道上的桥接分类。
 *
 * /api/agent/chat 的每一帧 SSE 都会被持久化成 `chat.sse.<event-name>`
 * （backend `trajectory_events.go` 的 chatSSEStreamEventPrefix），并在
 * 会话 runtime/stream 上按同一 seq 重放。但两侧的语义分工不同：
 *
 * - 正文/推理由总线事件（`assistant_delta` / `assistant.reasoning`）承担，
 *   它们与服务端 provider 的 stream_id + sequence 对齐，跨通道去重键相同。
 *   `chat.sse.chunk` / `chat.sse.reasoning` 携带的是**同一段文本**（实测
 *   chunk 的 sequence 与 assistant_delta 相同，reasoning 却差 1），因此这里
 *   **不把它们当增量**——否则同一段推理会被追加两次。
 * - `chat.sse.reasoning` 连「阶段推进」都不能做：它与 `assistant.reasoning`
 *   在日志里**逐帧成对**（实测 2109 帧会话：156 对，间隔恒为 1）。增量侧把
 *   尾段标成 `running: true`，这里若再收尾一次，同一个推理行会被两路写成
 *   「跑/停」交替——实测该标志在推理阶段翻转 312 次。收尾只由阶段出口
 *   （首个 `chunk` / `observation` / 工具帧）负责。
 * - 工具生命周期只有这一路有事件（`tool_call`/`tool_start`/`tool_end`），
 *   且都在实时循环里逐帧发出（handler.go 的 `emitter.Emit(streamEventName(...))`），
 *   正是「推理结束 → 工具执行」这段 UI 目前完全看不到的缺口。
 *
 * 归类只做「事件名 → 桥接种类」的映射，不读 payload；payload 解析沿用
 * `buildToolSegmentFromPayload`（tool / tool_call / delta 三种载体都覆盖）。
 */
export type RuntimeBridgeKind =
  | { kind: "tool"; status: RuntimeBridgeToolStatus }
  // 阶段推进：帧本身不携带可渲染内容（observation 只有工具名，chunk/reasoning
  // 的正文另有增量通道），只用来把仍在跑的推理段收尾。
  | { kind: "phase" };

/**
 * chat SSE 帧名 = 契约前缀 + kind。
 *
 * 帧名不在注册表里（contract.go 明确：帧名 = 前缀 + kind，是前缀派生而非封闭
 * 枚举），但前缀本身是契约常量，生成物与后端 `ChatSSEEventPrefix` 同源。
 */
function chatSseFrame(kind: string): string {
  return `${CHAT_SSE_EVENT_PREFIX}${kind}`;
}

export function getRuntimeBridgeKind(
  eventType: string,
): RuntimeBridgeKind | null {
  switch (eventType) {
    // 实时工具生命周期：agent loop 在执行每个工具的当下就会把
    // tool.requested / tool.completed 发到 runtime 总线（internal/agent/loop.go
    // 的 emitRuntimeEvent），事件存储把它们落成 tool_started / tool_finished。
    // 这两条事件过去被当成「与 chat.sse 共享 seq 的空洞」直接跳过，工具行只能
    // 等回合末由证据尾巴一次性补齐（实测 21 帧挤在末尾 18ms 内）。这里按真实
    // provider call id 建行，随后到达的 chat.sse.tool_* 帧按同一 id upsert 合并，
    // 因此既不会重复建行，也不必再等回合结束。
    //
    // 契约锚定：点形态 `tool.requested` / `tool.completed` 即注册表的 chat_bridge
    // 通道（`RUNTIME_EVENT_CHAT_BRIDGE_TYPES`）；`tool_started` / `tool_finished`
    // 是 store 侧改名（handler.go 的 mapRuntimeEventToSession），注册表中通道为 0。
    // 二者必须都被归类——event-contract.test.ts 门禁断言。
    case "tool_started":
    case "tool.requested":
      return { kind: "tool", status: "started" };
    case "tool_finished":
    case "tool.completed":
      return { kind: "tool", status: "finished" };
    case chatSseFrame("tool_start"):
      return { kind: "tool", status: "started" };
    case chatSseFrame("tool_call"):
      return { kind: "tool", status: "running" };
    case chatSseFrame("tool_end"):
      return { kind: "tool", status: "finished" };
    // observation 的 payload 只带工具**名**（`payload.tool` 是字符串，无 id，
    // 见 buildObservationEventPayloads），据它建行会退化成名叫 “tool” 的错行。
    // 工具行的收尾由同一次观测的 tool_end 完成，这里只当「阶段推进」信号。
    case chatSseFrame("observation"):
      return { kind: "phase" };
    case chatSseFrame("chunk"):
      return { kind: "phase" };
    // chat.sse.reasoning 刻意不归类（见上方说明）：它是增量帧的孪生副本，
    // 不是阶段出口，收尾会与 assistant.reasoning 的「仍在推理」标记打架。
    default:
      return null;
  }
}

/**
 * 打字机增量的 turn 归属判定（两条投递通道共用同一语义）。
 *
 * 语义与 `applyRuntimeDeltaToThread` / `RuntimeDeltaCoordinator.claim` 一致：
 * **「未知」不等于「其他 turn」**。事件没带 turn 身份时放行——provider 未暴露
 * 身份（见 coordinator 注释）、后端 `loop.go` 仅在 `turnID != ""` 时注入
 * `turn_id`，都会产生「事件无 turn 身份」的常态；此时若按严格相等拒绝，
 * 真后端的增量会在 hook 层被整条丢弃，打字机退化成「流结束后一次性定型」。
 * 只有两边都明确且不一致才拒绝，避免把别的 turn 的增量写进当前消息。
 */
export function matchesActiveTurn(
  activeTurnId: string | undefined,
  eventTurnId: string | undefined,
): boolean {
  const active = activeTurnId?.trim() ?? "";
  const eventTurn = eventTurnId?.trim() ?? "";
  if (!active || !eventTurn) {
    return true;
  }
  return active === eventTurn;
}

export function getRuntimeDeltaKeyFromEvent(
  event: SessionRuntimeEvent,
): string {
  const kind = getRuntimeDeltaKind(event.type);
  return kind ? getRuntimeDeltaKey(event.payload, kind) : "";
}
