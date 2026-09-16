// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 2026-09-15：live 通道相关逻辑（打字机增量、流式消息定位、`chat.sse.*` 桥接帧）
// 再拆到 ./events-live，本文件只保留与 live 无关的线程归约（P0-2 行数门禁）。

import { type Artifact, type ChatMessage, type MessageSegment, type Thread } from "@/data/mock";
import { type SessionRuntimeEvent } from "@/types/runtime";

import { getRuntimeBridgeKind } from "./deltas";
import { applyChatSseBridgeFrame, updateLatestAssistantMessage } from "./events-live";
import { buildGeneratedImagePlaceholderSegment, upsertGeneratedImageSegment } from "./generated-images";
import { buildRuntimeEventKey, buildSessionRuntimeEventsArtifact, MAX_RUNTIME_EVENTS } from "./history-artifacts";
import { getRuntimeEventSeq } from "./sessions";
import { mergeUniqueStrings, upsertArtifact } from "./shared";
import { applyTodoSnapshotToThread } from "./todos";

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
  /** 本会话当前在途回合（调用方持有的本轮 chat turn id）；桥接帧据此补建占位消息。 */
  activeTurnId?: string,
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
    nextThread = applyChatSseBridgeFrame(nextThread, event, bridgeFrame, activeTurnId);
  }

  // 任务面板（方案 §5.2 通道 A）：整值 LWW，不解析文本摘要；与 todo 无关的事件
  // 由折叠函数原样返回引用（折叠规则与坏载荷降级都在 lib/thread-state/todos.ts）。
  nextThread = applyTodoSnapshotToThread(nextThread, event);

  return nextThread;
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
