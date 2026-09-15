// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type ChatMessage, type MessageSegment, type Thread } from "@/data/mock";
import { type AgentChatStreamChunkPayload, type SessionRuntimeEvent } from "@/types/runtime";

import { buildGeneratedImagePlaceholderSegment, upsertGeneratedImageSegment } from "./generated-images";
import { buildRuntimeEventKey, buildSessionRuntimeEventsArtifact, MAX_RUNTIME_EVENTS } from "./history-artifacts";
import { getRuntimeBridgeKind, getRuntimeDeltaKind, matchesActiveTurn, type RuntimeBridgeKind } from "./deltas";
import { closeRunningReasoningSegments, STREAM_PLACEHOLDER_TEXT, type ToolMessageSegment } from "./messages";
import { getRuntimeEventSeq } from "./sessions";
import { getToolName, mergeUniqueStrings, upsertArtifact } from "./shared";
import { buildToolSegmentFromPayload, getToolCallId, getToolErrorMessage, upsertToolSegment } from "./tools";

export function appendArtifactToMessage(
  thread: Thread,
  messageId: string,
  artifact: Artifact,
) {
  const nextThread = updateThreadMessage(thread, messageId, (message) => ({
    ...message,
    relatedArtifactIds: mergeUniqueStrings(
      ...(message.relatedArtifactIds ?? []),
      artifact.id,
    ),
  }));

  return {
    ...nextThread,
    artifacts: upsertArtifact(nextThread.artifacts, artifact),
  };
}

export function applyRuntimeEventToThread(
  thread: Thread,
  sessionId: string,
  events: SessionRuntimeEvent[],
  event: SessionRuntimeEvent,
) {
  const transport: Thread["transport"] =
    thread.transport === "error" ? "error" : "live";
  const nextArtifact = buildSessionRuntimeEventsArtifact(sessionId, events);
  let nextThread: Thread = {
    ...thread,
    updatedAt: new Date().toISOString(),
    sessionId,
    transport,
    runtimeEventCount: events.length,
    lastRuntimeEventType: event.type,
    runtimeSource:
      thread.runtimeSource ?? event.agent_name ?? event.tool_name ?? "runtime",
    artifacts: upsertArtifact(thread.artifacts, nextArtifact),
  };

  // Keep the historical snapshot behavior for image progress.  Text and
  // reasoning deltas are gated to the live path, but image placeholders are
  // also useful while replaying a session that was restored mid-generation.
  if (event.type === "assistant.image_progress") {
    const imageSegment = buildGeneratedImagePlaceholderSegment(event.payload);
    if (imageSegment) {
      nextThread = updateLatestAssistantMessage(nextThread, (message) => ({
        ...message,
        segments: upsertGeneratedImageSegment(message.segments, imageSegment),
      }));
    }
  }

  // 方案C：会话 runtime/stream 上的 `chat.sse.*` 桥接帧（工具生命周期 /
  // 阶段推进）。与 image_progress 一样走「始终生效」的路径：它不写增量文本，
  // 只把工具行补进消息段、把跑完的推理段收尾，历史回放的安全由
  // isLiveAssistantMessage 的 streaming 闸门与上游 after 游标共同保证。
  const bridgeFrame = getRuntimeBridgeKind(event.type);
  if (bridgeFrame) {
    nextThread = applyChatSseBridgeFrame(nextThread, event, bridgeFrame);
  }

  return nextThread;
}

/**
 * 方案B：把 runtime/stream 实时到达的打字机增量事件应用到 thread。
 *
 * 只在“正在请求中”（渲染 gate=true）时由 useSessionRuntimeStream 调用；
 * 历史回放/reload 不会触发（否则会把旧 turn 的增量误渲染到当前消息）。
 * 覆盖三类增量：
 * - assistant_delta        → 最新 assistant 消息 text segment 追加增量
 * - assistant_reasoning    → 最新 assistant 消息 reasoning segment 追加增量
 * - assistant.image_progress → 图片生成占位段 upsert（原 applyRuntimeEventToThread 迁移）
 *
 * 与 /api/agent/chat 的最终 result 天然不冲突：result 到达时 onChunk 以
 * 完整文本重建 text segment（替换而非追加），文本不会翻倍。
 */
export function applyRuntimeDeltaToThread(
  thread: Thread,
  event: SessionRuntimeEvent,
  expectedTurnId?: string,
): Thread {
  const eventTurnId = getRuntimeEventTurnId(event);
  // 与 useSessionRuntimeStream 的实时门控共用同一判定（见 matchesActiveTurn）：
  // 事件未携带 turn 身份时视为「未知」而非「其他 turn」。
  if (!matchesActiveTurn(expectedTurnId, eventTurnId)) {
    return thread;
  }

  const updateLiveAssistant = (
    updater: (message: ChatMessage) => ChatMessage,
  ) => {
    let applied = false;
    const nextThread = updateLatestAssistantMessage(
      thread,
      (message) => {
        applied = true;
        return updater(message);
      },
      (message) => isLiveAssistantMessage(message, eventTurnId),
    );
    return applied ? nextThread : thread;
  };

  // 分类统一走 getRuntimeDeltaKind：它是「事件名 → 增量种类」的唯一真源
  // （含 `assistant.delta` 等总线双拼写别名），避免这里再维护一份 event.type
  // 白名单——两份名单一旦漂移，dot 形态的增量会在本层被 default 静默吞掉。
  switch (getRuntimeDeltaKind(event.type)) {
    case "text":
      return appendAssistantTextDelta(thread, event, updateLiveAssistant);
    case "reasoning":
      return appendAssistantReasoningDelta(thread, event, updateLiveAssistant);
    case "image":
      return appendAssistantImageProgress(thread, event, updateLiveAssistant);
    default:
      return thread;
  }
}

export function getRuntimeEventTurnId(event: SessionRuntimeEvent): string {
  const payload = event.payload;
  if (!payload) {
    return "";
  }
  for (const key of ["turn_id", "turnId", "turn"]) {
    const value = payload[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function appendAssistantTextDelta(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (
    updater: (message: ChatMessage) => ChatMessage,
  ) => Thread,
): Thread {
  const deltaText = readTextDelta(event.payload);
  if (!deltaText) {
    return thread;
  }
  return updateLiveAssistant((message) => {
    const segments = appendTextToMessageSegments(message.segments, deltaText);
    return { ...message, segments };
  });
}

function appendAssistantReasoningDelta(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (
    updater: (message: ChatMessage) => ChatMessage,
  ) => Thread,
): Thread {
  const payload = event.payload ?? {};
  const reasoningBlock =
    payload.reasoning && typeof payload.reasoning === "object"
      ? (payload.reasoning as Record<string, unknown>)
      : null;
  const deltaText =
    readRawTextValue(reasoningBlock ?? {}, "summary", "content", "delta") ||
    readRawTextValue(payload, "content", "delta");
  if (!deltaText) {
    return thread;
  }
  return updateLiveAssistant((message) => {
    return {
      ...message,
      segments: appendReasoningToMessageSegments(message.segments, deltaText),
    };
  });
}

function appendAssistantImageProgress(
  thread: Thread,
  event: SessionRuntimeEvent,
  updateLiveAssistant: (
    updater: (message: ChatMessage) => ChatMessage,
  ) => Thread,
): Thread {
  const imageSegment = buildGeneratedImagePlaceholderSegment(event.payload);
  if (!imageSegment) {
    return thread;
  }
  return updateLiveAssistant((message) => {
    return {
      ...message,
      segments: upsertGeneratedImageSegment(message.segments, imageSegment),
    };
  });
}

function readTextDelta(payload: Record<string, unknown> | undefined) {
  if (!payload) {
    return "";
  }
  return (
    readRawTextValue(payload, "delta", "content") ||
    (payload.text && typeof payload.text === "object"
      ? readRawTextValue(payload.text as Record<string, unknown>, "content", "delta")
      : "")
  );
}

/** 不 trim 的文本提取：打字机增量必须保留原始空白（delta 语义）。 */
function readRawTextValue(source: Record<string, unknown>, ...keys: string[]) {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  return "";
}

function appendTextToMessageSegments(
  segments: MessageSegment[],
  delta: string,
): MessageSegment[] {
  const nextSegments = [...segments];
  for (let index = nextSegments.length - 1; index >= 0; index--) {
    const segment = nextSegments[index];
    if (segment.type !== "text") {
      continue;
    }
    const previous = segment.content;
    const base = previous === STREAM_PLACEHOLDER_TEXT ? "" : previous;
    nextSegments[index] = { ...segment, content: base + delta };
    return nextSegments;
  }
  nextSegments.push({ type: "text", content: delta });
  return nextSegments;
}

function appendReasoningToMessageSegments(
  segments: MessageSegment[],
  delta: string,
): MessageSegment[] {
  const nextSegments = [...segments];
  for (let index = nextSegments.length - 1; index >= 0; index--) {
    const segment = nextSegments[index];
    if (segment.type !== "reasoning") {
      continue;
    }
    const previous = segment.content;
    // A persisted/final reasoning block may be followed by the first live
    // block for this turn.  Keep the visual separator used by the chat SSE
    // path, while preserving raw chunk boundaries once streaming is running.
    const separator =
      previous.length > 0 && segment.running !== true && !previous.endsWith("\n")
        ? "\n"
        : "";
    nextSegments[index] = {
      ...segment,
      content: previous + separator + delta,
      running: true,
    };
    return nextSegments;
  }
  nextSegments.push({ type: "reasoning", content: delta, running: true });
  return nextSegments;
}

export function buildStreamingMessageSegments(
  text: string,
  _source: string,
  reasoning: string,
  options?: {
    status?: "streaming" | "stopped";
    reasoningRunning?: boolean;
  },
) {
  // §12.1.4：没有正文就不产文本段——历史版本会塞一个 `...` 占位段，
  // 首块到达前它会在正文区渲染出一条真实的「...」行（空行 / 噪声）。
  const segments: MessageSegment[] = [];

  if (text) {
    segments.push({ type: "text", content: text });
  }

  if (reasoning.trim()) {
    segments.push({
      type: "reasoning",
      content: reasoning.trim(),
      running: options?.reasoningRunning === true,
    });
  }

  if (options?.status === "stopped") {
    segments.push({
      type: "callout",
      title: "Response stopped",
      tone: "warning",
      content:
        "Generation was stopped locally. Partial output is preserved so the next turn can continue from this point.",
    });
  }

  return segments;
}

export function createStreamingAssistantMessage(
  messageId: string,
  artifactIds: string[],
  runtimeTurnId?: string,
) {
  return {
    id: messageId,
    role: "assistant" as const,
    author: "Runtime stream",
    label: "streaming",
    runtimeTurnId,
    streaming: true,
    relatedArtifactIds: artifactIds,
    // §12.1.4：流式占位不落成文本段；首个 text delta 到达时由
    // `appendTextToMessageSegments` 直接追加（没有文本段时 push 新段）。
    segments: [] as MessageSegment[],
  };
}

export function isRuntimePayload(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && Object.keys(value).length > 0;
}

export function mergeRuntimeEvent(
  existingEvents: SessionRuntimeEvent[],
  nextEvent: SessionRuntimeEvent,
) {
  const nextSeq = getRuntimeEventSeq(nextEvent);
  if (
    nextSeq > 0 &&
    existingEvents.some((event) => getRuntimeEventSeq(event) === nextSeq)
  ) {
    // `payload.seq` is the session EventStore identity.  Do not include
    // timestamp/type in this comparison: a reconnect may deserialize the same
    // row with a different representation, but it must still be idempotent.
    return existingEvents;
  }
  const eventKey = buildRuntimeEventKey(nextEvent);
  if (
    nextSeq <= 0 &&
    existingEvents.some((event) => buildRuntimeEventKey(event) === eventKey)
  ) {
    return existingEvents;
  }
  return [...existingEvents, nextEvent].slice(-MAX_RUNTIME_EVENTS);
}

export function updateThreadMessage(
  thread: Thread,
  messageId: string,
  updater: (message: ChatMessage) => ChatMessage,
) {
  return {
    ...thread,
    messages: thread.messages.map((message) =>
      message.id === messageId ? updater(message) : message,
    ),
  };
}

function findLatestAssistantMessage(
  thread: Thread,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  for (let index = thread.messages.length - 1; index >= 0; index--) {
    const message = thread.messages[index];
    if (message.role !== "assistant" || !predicate(message)) {
      continue;
    }
    return { index, message };
  }

  return null;
}

function replaceMessageSegments(
  thread: Thread,
  messageIndex: number,
  segments: MessageSegment[],
  lastRuntimeEventType?: string,
) {
  return {
    ...thread,
    ...(lastRuntimeEventType ? { lastRuntimeEventType } : {}),
    messages: thread.messages.map((message, index) =>
      index === messageIndex ? { ...message, segments } : message,
    ),
  };
}

function updateLatestAssistantMessage(
  thread: Thread,
  updater: (message: ChatMessage) => ChatMessage,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  const target = findLatestAssistantMessage(thread, predicate);
  if (!target) {
    return thread;
  }
  return {
    ...thread,
    messages: thread.messages.map((current, currentIndex) =>
      currentIndex === target.index ? updater(current) : current,
    ),
  };
}

/**
 * 把一条 `chat.sse.*` 桥接帧应用到「当前仍在 streaming 的助手消息」上。
 *
 * - tool（tool_start / tool_call / tool_end）→ 归并工具行：键取
 *   `tool_call.id`（帧里还有 `tool` / `delta` 两种载体，buildToolSegmentFromPayload
 *   已覆盖），状态按帧类型与 metadata.error 得到 started → running →
 *   finished/error。与 /api/agent/chat 通道用同一个 upsert，两条通道先后到达
 *   只会收敛成同一行。
 * - text / reasoning（chunk / reasoning / observation）→ 只做阶段推进：
 *   把仍在跑的推理段收尾。正文与推理文本**不在这里追加**——同一段文本已由
 *   `assistant_delta` / `assistant.reasoning` 写入（这正是 `getRuntimeBridgeKind`
 *   不把它们当增量的原因），在这里再追加一次会让每段内容翻倍。
 */
function applyChatSseBridgeFrame(
  thread: Thread,
  event: SessionRuntimeEvent,
  frame: RuntimeBridgeKind,
): Thread {
  const payload = event.payload as AgentChatStreamChunkPayload | undefined;
  if (!payload) {
    return thread;
  }
  const target = findLatestAssistantMessage(thread, (message) =>
    isLiveAssistantMessage(message, getRuntimeEventTurnId(event)),
  );
  if (!target) {
    return thread;
  }

  const closeReasoning = () => {
    const segments = closeRunningReasoningSegments(target.message.segments);
    // 没有在跑的推理段就返回原引用：单回合上千帧，逐帧换身份会让下游 memo 全部失效。
    return segments === target.message.segments
      ? thread
      : replaceMessageSegments(thread, target.index, segments);
  };

  if (frame.kind !== "tool") {
    return closeReasoning();
  }

  const toolName = getToolName(payload);
  // 既无工具调用 id、也无真实工具名（旧帧/残缺帧）时不落行：upsertToolSegment
  // 的键会退化成兜底名 “tool”，把互不相干的调用合并成一行假工具。
  if (!getToolCallId(payload) && toolName === "tool") {
    return closeReasoning();
  }

  const status: ToolMessageSegment["status"] = getToolErrorMessage(payload)
    ? "error"
    : frame.status;
  const segments = upsertToolSegment(
    closeRunningReasoningSegments(target.message.segments),
    buildToolSegmentFromPayload(payload, status),
  );
  // 与直连通道同形（`tool_end:shell`）：线程条与历史重载键读的是同一个字段。
  return replaceMessageSegments(
    thread,
    target.index,
    segments,
    `${event.type.replace(/^chat\.sse\./, "")}:${toolName}`,
  );
}

function isLiveAssistantMessage(
  message: ChatMessage,
  eventTurnId: string,
): boolean {
  // New messages carry an explicit streaming bit. The label fallback keeps
  // compatibility with callers/tests created before the bit was introduced.
  if (message.streaming === false || message.interrupted) {
    return false;
  }
  if (message.streaming !== true && message.label !== "streaming") {
    return false;
  }
  // 与 matchesActiveTurn / RuntimeDeltaCoordinator.claim 共用同一语义：
  // 「未知」不等于「其他 turn」，只有两边都明确且不一致才拒绝。
  //
  // 旧实现要求「两边都为空」才放行，于是真后端最常见的两种形态——消息带
  // chat turn 身份而 runtime 事件缺 turn_id，或反过来（两条通道的 turn 身份
  // 空间本就不同）——会被整条拒绝：增量帧全部到达却一帧也写不进消息，
  // 打字机退化成「turn 结束后一次性定型」（实测两路各约 1000 帧、DOM 全程
  // 不动，24s 时整块蹦出）。回放安全由 hook 层 renderLiveDeltas 闸门兜底：
  // 只在请求进行中应用增量，历史回放/reload 不走这条路径。
  if (message.runtimeTurnId && eventTurnId) {
    return message.runtimeTurnId === eventTurnId;
  }
  return true;
}
