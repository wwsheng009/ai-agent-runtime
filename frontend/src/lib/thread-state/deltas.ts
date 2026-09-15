// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type SessionRuntimeEvent } from "@/types/runtime";

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

export function getRuntimeDeltaKind(
  eventType: string,
): RuntimeDeltaKind | null {
  switch (eventType) {
    // 总线双拼写别名：runtimeobserve/known_types.go 明确记录
    // `assistant.delta` 与 `assistant_delta` 两种形态都在用；漏归类会让
    // 打字机增量在 dot 形态下整条静默丢弃（既不入消息也不占去重键）。
    case "assistant_delta":
    case "assistant.delta":
      return "text";
    case "assistant_reasoning":
    case "assistant.reasoning":
    case "assistant.reasoning_delta":
      return "reasoning";
    case "assistant.image_progress":
      return "image";
    default:
      return null;
  }
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
  | { kind: "text" };

export function getRuntimeBridgeKind(
  eventType: string,
): RuntimeBridgeKind | null {
  switch (eventType) {
    case "chat.sse.tool_start":
      return { kind: "tool", status: "started" };
    case "chat.sse.tool_call":
      return { kind: "tool", status: "running" };
    case "chat.sse.tool_end":
      return { kind: "tool", status: "finished" };
    // observation 的 payload 只带工具**名**（`payload.tool` 是字符串，无 id，
    // 见 buildObservationEventPayloads），据它建行会退化成名叫 “tool” 的错行。
    // 工具行的收尾由同一次观测的 tool_end 完成，这里只当「阶段推进」信号。
    case "chat.sse.observation":
      return { kind: "text" };
    case "chat.sse.chunk":
      return { kind: "text" };
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
