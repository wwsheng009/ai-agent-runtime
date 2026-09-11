// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type ChatMessage, type MessageSegment, type Thread } from "@/data/mock";
import { type SessionRuntimeEvent } from "@/types/runtime";

import { buildGeneratedImagePlaceholderSegment, upsertGeneratedImageSegment } from "./generated-images";
import { buildRuntimeEventKey, buildSessionRuntimeEventsArtifact, MAX_RUNTIME_EVENTS } from "./history-artifacts";
import { STREAM_PLACEHOLDER_TEXT } from "./messages";
import { getRuntimeEventSeq } from "./sessions";
import { mergeUniqueStrings, upsertArtifact } from "./shared";

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
  if (
    expectedTurnId &&
    eventTurnId &&
    expectedTurnId !== eventTurnId
  ) {
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

  switch (event.type) {
    case "assistant_delta":
      return appendAssistantTextDelta(thread, event, updateLiveAssistant);
    case "assistant_reasoning":
    case "assistant.reasoning":
    case "assistant.reasoning_delta":
      return appendAssistantReasoningDelta(thread, event, updateLiveAssistant);
    case "assistant.image_progress":
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
  const segments: MessageSegment[] = [
    {
      type: "text",
      content: text || STREAM_PLACEHOLDER_TEXT,
    },
  ];

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
    segments: [
      {
        type: "text" as const,
        content: STREAM_PLACEHOLDER_TEXT,
      },
    ],
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

function updateLatestAssistantMessage(
  thread: Thread,
  updater: (message: ChatMessage) => ChatMessage,
  predicate: (message: ChatMessage) => boolean = () => true,
) {
  for (let index = thread.messages.length - 1; index >= 0; index--) {
    const message = thread.messages[index];
    if (message.role !== "assistant" || !predicate(message)) {
      continue;
    }
    return {
      ...thread,
      messages: thread.messages.map((current, currentIndex) =>
        currentIndex === index ? updater(current) : current,
      ),
    };
  }

  return thread;
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
  // Once a request has an identity, an unlabelled or differently labelled
  // durable event is unsafe: it may be a replay from an earlier turn.  Never
  // fall back to the old global "currently responding" gate.
  if (message.runtimeTurnId && eventTurnId) {
    return message.runtimeTurnId === eventTurnId;
  }
  return !message.runtimeTurnId && !eventTurnId;
}
