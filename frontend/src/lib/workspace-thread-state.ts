import {
  type Artifact,
  type ChatMessage,
  type MessageSegment,
  type Thread,
} from "@/data/mock";
import { buildRuntimeUrl } from "@/api/runtime/shared";
import {
  type AgentChatStreamChunkPayload,
  type RuntimeSessionRecord,
  type SessionHistoryMessage,
  type SessionHistoryResponse,
  type SessionRuntimeEvent,
} from "@/types/runtime";
import { normalizeSessionId } from "@/lib/session-id";

const MAX_RUNTIME_EVENTS = 100;
const STREAM_PLACEHOLDER_TEXT = "...";

type HistoryMessageMapping = {
  artifacts: Artifact[];
  message: ChatMessage;
};

type GeneratedImageAttachments = {
  artifacts: Artifact[];
  segments: MessageSegment[];
};

type GeneratedImageSegment =
  | Extract<MessageSegment, { type: "image" }>
  | Extract<MessageSegment, { type: "image-placeholder" }>;

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
    case "assistant_delta":
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

export function getRuntimeDeltaKeyFromEvent(
  event: SessionRuntimeEvent,
): string {
  const kind = getRuntimeDeltaKind(event.type);
  return kind ? getRuntimeDeltaKey(event.payload, kind) : "";
}

export type ToolMessageSegment = Extract<MessageSegment, { type: "tool" }>;

export type ReasoningMessageSegment = Extract<
  MessageSegment,
  { type: "reasoning" }
>;

/**
 * Reconcile a live assistant buffer with a terminal result without allowing a
 * shorter terminal snapshot to erase text that was already displayed.  When
 * the two buffers diverge, the longer one wins; when one is a prefix of the
 * other this also preserves the normal authoritative-result extension case.
 */
export function reconcileRuntimeText(
  liveText: string | null | undefined,
  resultText: string | null | undefined,
): string {
  const live = typeof liveText === "string" ? liveText : "";
  const result = typeof resultText === "string" ? resultText : "";
  if (!live.trim()) {
    return result;
  }
  if (!result.trim()) {
    return live;
  }
  if (live === result) {
    return result;
  }
  if (live.startsWith(result)) {
    return live;
  }
  if (result.startsWith(live)) {
    return result;
  }
  return result.length >= live.length ? result : live;
}

/** Extract text already rendered for one assistant message (placeholder-safe). */
export function getAssistantMessageText(message: ChatMessage): string {
  const values = message.segments
    .filter((segment): segment is Extract<MessageSegment, { type: "text" }> =>
      segment.type === "text",
    )
    .map((segment) => segment.content);
  if (
    message.streaming &&
    values.length === 1 &&
    values[0] === STREAM_PLACEHOLDER_TEXT
  ) {
    return "";
  }
  return values.join("");
}

/** Extract reasoning already rendered for one assistant message. */
export function getAssistantMessageReasoning(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is ReasoningMessageSegment =>
        segment.type === "reasoning",
    )
    .map((segment) => segment.content)
    .join("");
}

type AssistantMessageSegmentOptions = {
  status?: "streaming" | "stopped";
  reasoningRunning?: boolean;
  existingSegments?: MessageSegment[];
  generatedImages?: GeneratedImageAttachments;
};

export function buildGeneratedImagePlaceholderSegment(
  metadata: Record<string, unknown> | null | undefined,
): Extract<MessageSegment, { type: "image-placeholder" }> | null {
  const source = normalizeGeneratedImageProgressSource(metadata);
  if (!source) {
    return null;
  }

  const imageId = normalizeGeneratedImageToken(
    readFirstTextValue(source, "sanitized_id", "image_id", "item_id", "id"),
  );
  if (!imageId) {
    return null;
  }

  const phase = normalizeGeneratedImagePhase(
    readFirstTextValue(source, "phase", "status"),
  );
  const caption =
    readFirstTextValue(source, "caption", "revised_prompt", "revisedPrompt") ||
    undefined;
  const progress = readFirstNumberValue(source, "progress", "progress_ratio");
  const errorMessage =
    phase === "failed"
      ? readFirstTextValue(source, "error", "error_message", "message") || undefined
      : undefined;

  return {
    type: "image-placeholder",
    imageId,
    phase,
    progress,
    caption,
    errorMessage,
  };
}

export function upsertGeneratedImageSegment(
  segments: MessageSegment[],
  nextSegment: GeneratedImageSegment,
) {
  const nextImageId = getGeneratedImageSegmentId(nextSegment);
  if (!nextImageId) {
    return [...segments, nextSegment];
  }

  const merged: MessageSegment[] = [];
  let matched = false;

  for (const segment of segments) {
    if (!isGeneratedImageSegment(segment)) {
      merged.push(segment);
      continue;
    }

    const currentImageId = getGeneratedImageSegmentId(segment);
    if (currentImageId !== nextImageId) {
      merged.push(segment);
      continue;
    }

    if (!matched) {
      matched = true;
      if (nextSegment.type === "image-placeholder" && segment.type === "image") {
        merged.push(segment);
      } else {
        merged.push(nextSegment);
      }
    }
  }

  if (!matched) {
    merged.push(nextSegment);
  }

  return merged;
}

export function buildAssistantMessageSegments(
  text: string,
  source: string,
  reasoning: string,
  options?: AssistantMessageSegmentOptions,
) {
  let segments = buildStreamingMessageSegments(text, source, reasoning, {
    status: options?.status,
    reasoningRunning: options?.reasoningRunning,
  });

  for (const segment of options?.existingSegments ?? []) {
    if (isGeneratedImageSegment(segment)) {
      segments = upsertGeneratedImageSegment(segments, segment);
    } else if (segment.type === "tool") {
      segments = upsertToolSegment(segments, segment);
    }
  }

  for (const segment of options?.generatedImages?.segments ?? []) {
    if (isGeneratedImageSegment(segment)) {
      segments = upsertGeneratedImageSegment(segments, segment);
      continue;
    }
    segments.push(segment);
  }

  return segments;
}

export function buildGeneratedImageAttachments(
  sessionId: string,
  metadata: Record<string, unknown> | null | undefined,
) {
  const attachments: GeneratedImageAttachments = {
    artifacts: [],
    segments: [],
  };

  const root = metadata && typeof metadata === "object" ? metadata : undefined;
  const rawImages = root?.["generated_images"];
  if (Array.isArray(rawImages)) {
    rawImages.forEach((item, index) => {
      if (!item || typeof item !== "object") {
        return;
      }

      const value = item as Record<string, unknown>;
      const rawId = readFirstTextValue(value, "id") || `generated-image-${index + 1}`;
      const imageId = normalizeGeneratedImageToken(rawId);
      const savedPath = readFirstTextValue(value, "saved_path", "savedPath");
      const basename =
        (savedPath ? filepathBase(savedPath) : "") ||
        `${imageId || "generated-image"}.png`;
      const artifactName = basename;
      const artifactPath = `runtime/generated-images/${artifactName}`;
      const prompt =
        readFirstTextValue(value, "revised_prompt", "revisedPrompt") || undefined;
      const contentName = imageId || stripFileExtension(artifactName);
      const src = buildGeneratedImageUrl(sessionId, contentName);
      const artifactId = buildGeneratedImageArtifactId(sessionId, contentName);
      const mimeType =
        readFirstTextValue(value, "mime_type", "mimeType") || "image/png";
      const summary = prompt
        ? `Generated image for ${truncateText(prompt, 96)}`
        : "Generated image saved from assistant output.";

      attachments.artifacts.push({
        id: artifactId,
        name: artifactName,
        path: artifactPath,
        summary,
        kind: "image",
        content: src,
        mimeType,
        byteCount: readFirstNumberValue(value, "byte_count", "byteCount"),
        sha256: readFirstTextValue(value, "sha256"),
        revisedPrompt: prompt,
      });

      attachments.segments.push({
        type: "image",
        src,
        alt: prompt || "Generated image",
        caption: prompt,
        artifactId,
        imageId: contentName,
      });
    });
  }

  const error = root ? readFirstTextValue(root, "generated_images_error") : "";
  if (error) {
    attachments.segments.push({
      type: "callout",
      title: "图片保存失败",
      tone: "warning",
      content: error,
    });
  }

  return attachments;
}

function isGeneratedImageSegment(
  segment: MessageSegment,
): segment is GeneratedImageSegment {
  return segment.type === "image" || segment.type === "image-placeholder";
}

function getGeneratedImageSegmentId(segment: GeneratedImageSegment) {
  if (segment.type === "image-placeholder") {
    return normalizeGeneratedImageToken(segment.imageId);
  }
  if (segment.imageId && segment.imageId.trim()) {
    return normalizeGeneratedImageToken(segment.imageId);
  }
  if (segment.artifactId && segment.artifactId.trim()) {
    const token = segment.artifactId.trim().split(":").pop() ?? segment.artifactId;
    return normalizeGeneratedImageToken(token);
  }
  return "";
}

function normalizeGeneratedImageProgressSource(
  metadata: Record<string, unknown> | null | undefined,
) {
  if (!metadata || typeof metadata !== "object") {
    return undefined;
  }
  const nested = metadata["image"];
  if (nested && typeof nested === "object" && !Array.isArray(nested)) {
    return nested as Record<string, unknown>;
  }
  return metadata;
}

function normalizeGeneratedImagePhase(value: string) {
  switch (value.trim().toLowerCase()) {
    case "partial":
    case "progress":
    case "streaming":
      return "partial";
    case "completed":
    case "complete":
    case "done":
      return "completed";
    case "failed":
    case "error":
      return "failed";
    default:
      return "started";
  }
}

function normalizeGeneratedImageToken(value: string) {
  return sanitizeArtifactToken(value) || "";
}

function readFirstTextValue(
  source: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (trimmed) {
        return trimmed;
      }
    }
  }
  return "";
}

function readFirstValue(
  source: Record<string, unknown>,
  ...keys: string[]
): unknown {
  for (const key of keys) {
    const value = source[key];
    if (value !== undefined && value !== null) {
      return value;
    }
  }
  return undefined;
}

function readFirstNumberValue(
  source: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
    if (typeof value === "string") {
      const parsed = Number(value);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return undefined;
}

function createJsonArtifact(
  id: string,
  filename: string,
  summary: string,
  payload: unknown,
): Artifact {
  return {
    id,
    name: filename,
    path: `runtime/${filename}`,
    summary,
    kind: "json",
    language: "json",
    content: JSON.stringify(payload, null, 2),
  };
}

function readHistoryMessageIdentity(
  message: SessionHistoryMessage,
): string | undefined {
  const metadata =
    message.metadata && typeof message.metadata === "object"
      ? message.metadata
      : undefined;
  if (!metadata) {
    return undefined;
  }
  for (const key of ["message_id", "id"] as const) {
    const raw = metadata[key];
    if (typeof raw === "string") {
      const trimmed = raw.trim();
      if (trimmed) {
        return trimmed;
      }
    }
  }
  return undefined;
}

function buildHistoryMessage(
  sessionId: string,
  index: number,
  message: SessionHistoryMessage,
  artifacts: Artifact[],
  generatedImageSegments: MessageSegment[],
): ChatMessage {
  const relatedArtifactIds = artifacts.map((artifact) => artifact.id);
  const stableId = readHistoryMessageIdentity(message);
  return {
    id: stableId || `${sessionId}-history-${index}`,
    role: message.role === "user" ? "user" : "assistant",
    author: getHistoryMessageAuthor(message.role),
    label: message.role || "runtime",
    relatedArtifactIds:
      relatedArtifactIds.length > 0 ? relatedArtifactIds : undefined,
    segments: [
      {
        type: "text",
        content: message.content?.trim() || "[empty message]",
      },
      ...generatedImageSegments,
    ],
  };
}

function mapSessionHistoryToMessages(
  sessionId: string,
  history: SessionHistoryMessage[] | null | undefined,
  existingMessages: ChatMessage[],
) {
  const usedMessageIds = new Set<string>();
  const normalizedHistory = normalizeSessionHistoryMessages(history);

  return normalizedHistory.map((item, index) => {
    const generatedImageAttachments =
      extractGeneratedImagesFromAssistantMessage(item, sessionId);
    const restoredArtifacts = buildHistoryArtifacts(
      sessionId,
      index,
      item,
      generatedImageAttachments.artifacts,
    );
    const fallback = buildHistoryMessage(
      sessionId,
      index,
      item,
      restoredArtifacts,
      generatedImageAttachments.segments,
    );
    const fallbackText = getPrimaryTextContent(fallback);

    const stableId = readHistoryMessageIdentity(item);
    const matched = existingMessages.find((message) => {
      if (usedMessageIds.has(message.id)) {
        return false;
      }
      if (stableId && message.id === stableId) {
        return true;
      }
      return (
        message.role === fallback.role &&
        getPrimaryTextContent(message) === fallbackText
      );
    });

    if (!matched) {
      return {
        artifacts: restoredArtifacts,
        message: fallback,
      } satisfies HistoryMessageMapping;
    }

    usedMessageIds.add(matched.id);
    if (stableId) {
      usedMessageIds.add(stableId);
    }
    const codeSegments = matched.segments.filter(
      (segment): segment is Extract<MessageSegment, { type: "code" }> =>
        segment.type === "code",
    );
    const relatedArtifactIds = mergeUniqueStrings(
      ...(matched.relatedArtifactIds ?? []),
      ...(fallback.relatedArtifactIds ?? []),
    );

    return {
      artifacts: restoredArtifacts,
      message: {
        ...matched,
        // Prefer durable runtime message_id once history exposes it.
        id: stableId || matched.id || fallback.id,
        role: fallback.role,
        author: matched.author || fallback.author,
        label: matched.label || fallback.label,
        relatedArtifactIds:
          relatedArtifactIds.length > 0 ? relatedArtifactIds : undefined,
        segments: [...fallback.segments, ...codeSegments],
      },
    } satisfies HistoryMessageMapping;
  });
}

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

export function applySessionHistoryToThread(
  thread: Thread,
  response: SessionHistoryResponse,
) {
  const sessionId =
    normalizeSessionId(response.session_id) ||
    normalizeSessionId(thread.sessionId);
  const transport: Thread["transport"] =
    thread.transport === "error" ? "error" : "live";
  const historyArtifact = buildSessionHistoryArtifact(response);
  const mappedHistory = mapSessionHistoryToMessages(
    sessionId,
    response.history,
    thread.messages,
  );
  const resolvedMessages =
    mappedHistory.length > 0
      ? mappedHistory.map((item) => item.message)
      : thread.messages;
  return {
    ...thread,
    updatedAt: new Date().toISOString(),
    sessionId,
    transport,
    lastError: thread.transport === "error" ? thread.lastError : null,
    messages: resolvedMessages,
    artifacts: upsertArtifacts(thread.artifacts, [
      historyArtifact,
      ...mappedHistory.flatMap((item) => item.artifacts),
    ]),
  };
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

export function getToolCallId(payload: AgentChatStreamChunkPayload) {
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const id = readFirstTextValue(payload.tool_call, "id", "tool_call_id");
    if (id) {
      return id;
    }
  }
  if (payload.tool && typeof payload.tool === "object") {
    const id = readFirstTextValue(payload.tool, "id", "tool_call_id");
    if (id) {
      return id;
    }
  }
  if (payload.delta && typeof payload.delta === "object") {
    const id = readFirstTextValue(payload.delta, "id");
    if (id) {
      return id;
    }
  }
  return "";
}

export function getToolArgumentsSummary(payload: AgentChatStreamChunkPayload) {
  for (const source of [
    payload.tool,
    payload.tool_call,
    payload.delta,
  ]) {
    if (!source || typeof source !== "object") {
      continue;
    }
    const args =
      readFirstValue(source, "args", "arguments", "input", "params") ??
      source["arguments_json"];
    if (args === undefined || args === null) {
      continue;
    }
    if (typeof args === "string") {
      const trimmed = args.trim();
      if (trimmed) {
        return trimmed.length > 320 ? `${trimmed.slice(0, 317)}...` : trimmed;
      }
      continue;
    }
    if (typeof args === "object") {
      try {
        const serialized = JSON.stringify(args);
        return serialized.length > 320
          ? `${serialized.slice(0, 317)}...`
          : serialized;
      } catch {
        return String(args);
      }
    }
  }
  return "";
}

export function getToolResultSummary(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool === "object") {
    const content = readFirstTextValue(payload.tool, "content", "result");
    if (content) {
      return truncateText(content, 240);
    }
  }
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const content = readFirstTextValue(
      payload.tool_call,
      "content",
      "result",
      "output",
    );
    if (content) {
      return truncateText(content, 240);
    }
  }
  if (payload.content && payload.content.trim()) {
    return truncateText(payload.content, 240);
  }
  return "";
}

export function getToolErrorMessage(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool === "object") {
    const error = readFirstTextValue(
      payload.tool,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  if (payload.tool_call && typeof payload.tool_call === "object") {
    const error = readFirstTextValue(
      payload.tool_call,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  if (payload.metadata && typeof payload.metadata === "object") {
    const error = readFirstTextValue(
      payload.metadata,
      "error",
      "error_message",
      "message",
    );
    if (error) {
      return error;
    }
  }
  return "";
}

export function buildToolSegmentFromPayload(
  payload: AgentChatStreamChunkPayload,
  status: ToolMessageSegment["status"],
): ToolMessageSegment {
  return {
    type: "tool",
    toolCallId: getToolCallId(payload) || undefined,
    name: getToolName(payload),
    status,
    argsSummary: getToolArgumentsSummary(payload) || undefined,
    resultSummary:
      status === "finished" ? getToolResultSummary(payload) || undefined : undefined,
    errorMessage:
      status === "error"
        ? getToolErrorMessage(payload) || "Tool execution failed."
        : undefined,
  };
}

export function getToolSegmentKey(segment: ToolMessageSegment) {
  return segment.toolCallId?.trim() || segment.name;
}

export function upsertToolSegment(
  segments: MessageSegment[],
  nextSegment: ToolMessageSegment,
) {
  const key = getToolSegmentKey(nextSegment);
  if (!key) {
    return [...segments, nextSegment];
  }

  const merged: MessageSegment[] = [];
  let matched = false;

  for (const segment of segments) {
    if (segment.type !== "tool") {
      merged.push(segment);
      continue;
    }

    const currentKey = getToolSegmentKey(segment);
    if (currentKey !== key) {
      merged.push(segment);
      continue;
    }

    if (!matched) {
      matched = true;
      merged.push({
        ...segment,
        ...nextSegment,
      });
    }
  }

  if (!matched) {
    merged.push(nextSegment);
  }

  return merged;
}

export function mergeRuntimeSessionsIntoThreads(
  threads: Thread[],
  sessions: RuntimeSessionRecord[],
) {
  const nextThreads = [...threads];
  let changed = false;

  for (const session of sessions) {
    const sessionId = normalizeSessionId(session?.id);
    if (!sessionId) {
      continue;
    }
    const existingIndex = nextThreads.findIndex(
      (thread) =>
        normalizeSessionId(thread.sessionId) === sessionId ||
        normalizeSessionId(thread.id) === sessionId,
    );
    const title =
      session.metadata?.title?.trim() || `Runtime session ${sessionId.slice(0, 10)}`;
    const summary =
      session.metadata?.summary?.trim() ||
      "Restored runtime session from /api/runtime/sessions.";
    const updatedAt = session.updatedAt || session.createdAt || new Date().toISOString();
    const tags = mergeUniqueStrings(
      "runtime-session",
      session.state ? `state:${session.state}` : null,
      ...(session.metadata?.lastAgent ? [`agent:${session.metadata.lastAgent}`] : []),
      ...(session.metadata?.lastSkill ? [`skill:${session.metadata.lastSkill}`] : []),
      ...(session.metadata?.title ? ["restored"] : []),
    );

    if (existingIndex < 0) {
      changed = true;
      nextThreads.push({
        id: sessionId,
        title,
        summary,
        updatedAt,
        status: mapSessionStateToThreadStatus(session.state),
        sessionId,
        transport: "live",
        runtimeSource: session.metadata?.lastAgent || session.metadata?.lastSkill || "runtime",
        lastError: null,
        tags,
        prompts: [
          "Sync the latest authoritative session history",
          "Inspect runtime evidence and restore points",
          "Continue this restored runtime session",
        ],
        messages: [],
        artifacts: [],
      });
      continue;
    }

    const current = nextThreads[existingIndex];
    const merged = {
      ...current,
      id: current.sessionId ? current.id : sessionId,
      title,
      summary,
      updatedAt,
      status: mapSessionStateToThreadStatus(session.state),
      sessionId,
      transport: current.transport === "error" ? "error" : "live",
      runtimeSource:
        current.runtimeSource ||
        session.metadata?.lastAgent ||
        session.metadata?.lastSkill ||
        "runtime",
      tags,
    } satisfies Thread;

    if (
      merged.title !== current.title ||
      merged.summary !== current.summary ||
      merged.updatedAt !== current.updatedAt ||
      merged.status !== current.status ||
      merged.sessionId !== current.sessionId ||
      merged.transport !== current.transport ||
      merged.runtimeSource !== current.runtimeSource ||
      merged.tags.join("|") !== current.tags.join("|")
    ) {
      changed = true;
      nextThreads[existingIndex] = merged;
    }
  }

  if (!changed) {
    return threads;
  }

  return [...nextThreads].sort((left, right) => {
    const leftTime = Date.parse(left.updatedAt);
    const rightTime = Date.parse(right.updatedAt);
    return rightTime - leftTime;
  });
}

export function buildTurnJsonArtifact(
  turnId: string,
  suffix: string,
  summary: string,
  payload: unknown,
) {
  return createJsonArtifact(
    `turn-${turnId}-${suffix}`,
    `${suffix}-${turnId}.json`,
    summary,
    payload,
  );
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

export function getErrorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export function getFirstArtifactId(thread: Thread | undefined) {
  return thread?.artifacts[0]?.id ?? null;
}

export function getRuntimeEventSeq(event: SessionRuntimeEvent) {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq)) {
    return rawSeq;
  }
  if (typeof rawSeq === "string") {
    const parsed = Number(rawSeq);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return 0;
}

export function getStreamTextDelta(payload: AgentChatStreamChunkPayload) {
  if (payload.type !== "text") {
    return "";
  }
  if (typeof payload.content === "string") {
    return payload.content;
  }
  if (payload.text && typeof payload.text.content === "string") {
    return payload.text.content;
  }
  return "";
}

export function getToolName(payload: AgentChatStreamChunkPayload) {
  if (payload.tool && typeof payload.tool.name === "string") {
    return payload.tool.name;
  }
  if (payload.tool_call && typeof payload.tool_call.name === "string") {
    return payload.tool_call.name;
  }
  return "tool";
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

export function mergeUniqueStrings(...values: Array<string | undefined | null>) {
  const merged = new Set<string>();
  for (const value of values) {
    if (!value) {
      continue;
    }
    merged.add(value);
  }
  return [...merged];
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

export function upsertArtifact(artifacts: Artifact[], artifact: Artifact) {
  const nextArtifacts = [...artifacts];
  const existingIndex = nextArtifacts.findIndex((item) => item.id === artifact.id);
  if (existingIndex >= 0) {
    nextArtifacts[existingIndex] = artifact;
    return nextArtifacts;
  }
  return [artifact, ...nextArtifacts];
}

export function upsertArtifacts(artifacts: Artifact[], nextItems: Artifact[]) {
  return nextItems.reduce((current, artifact) => upsertArtifact(current, artifact), artifacts);
}

function buildRuntimeEventKey(event: SessionRuntimeEvent) {
  const payload = event.payload ?? {};
  const streamID =
    typeof payload.stream_id === "string" ? payload.stream_id : "";
  const streamSequence =
    typeof payload.sequence === "number" || typeof payload.sequence === "string"
      ? String(payload.sequence)
      : "";
  return [
    getRuntimeEventSeq(event),
    streamID,
    streamSequence,
    event.type,
    event.trace_id ?? "",
    event.tool_name ?? "",
    event.timestamp,
  ].join(":");
}

function mapSessionStateToThreadStatus(
  state: string | undefined,
): Thread["status"] {
  switch ((state || "").trim().toLowerCase()) {
    case "archived":
    case "closed":
      return "review";
    case "draft":
      return "draft";
    default:
      return "active";
  }
}

function buildSessionHistoryArtifact(response: SessionHistoryResponse) {
  return createJsonArtifact(
    `session-history-${response.session_id}`,
    `session-history-${response.session_id}.json`,
    "Authoritative session history loaded from /api/runtime/sessions/{id}/history.",
    response,
  );
}

function buildSessionRuntimeEventsArtifact(
  sessionId: string,
  events: SessionRuntimeEvent[],
) {
  return createJsonArtifact(
    `session-runtime-events-${sessionId}`,
    `session-runtime-events-${sessionId}.json`,
    "Runtime events streamed from /api/runtime/sessions/{id}/runtime/stream.",
    {
      session_id: sessionId,
      count: events.length,
      events,
    },
  );
}

function normalizeSessionHistoryMessages(
  history: SessionHistoryMessage[] | null | undefined,
) {
  if (!Array.isArray(history)) {
    return [] as SessionHistoryMessage[];
  }

  return history.flatMap((item) => {
    if (!item || typeof item !== "object") {
      return [];
    }

    // Strip internal prompt-context snapshots (fact_ledger, compaction, etc.)
    // so they never leak into the user-facing thread UI.
    if (isInternalPromptContextMessage(item)) {
      return [];
    }

    return [
      {
        role: typeof item.role === "string" ? item.role : "",
        content: typeof item.content === "string" ? item.content : "",
        metadata:
          item.metadata && typeof item.metadata === "object"
            ? item.metadata
            : undefined,
      },
    ];
  });
}

const LEGACY_FACT_LEDGER_HEADER =
  "Verified fact ledger (authoritative over compacted prose):";

function isInternalPromptContextMessage(item: {
  role?: string;
  content?: string;
  metadata?: Record<string, unknown>;
}) {
  const metadata =
    item.metadata && typeof item.metadata === "object" ? item.metadata : null;
  if (metadata) {
    if (
      typeof metadata.context_stage === "string" &&
      metadata.context_stage.trim() !== ""
    ) {
      return true;
    }
    if (metadata.context_snapshot === true) {
      return true;
    }
  }

  const role = typeof item.role === "string" ? item.role.toLowerCase().trim() : "";
  if (role === "developer" || role === "system") {
    // Developer/system messages are prompt infrastructure, not chat turns.
    // Keep only explicit user/assistant/tool dialogue in the UI thread.
    return role === "developer";
  }

  const content = typeof item.content === "string" ? item.content.trim() : "";
  return content.startsWith(LEGACY_FACT_LEDGER_HEADER);
}

function buildHistoryArtifacts(
  sessionId: string,
  messageIndex: number,
  message: SessionHistoryMessage,
  generatedImageArtifacts: Artifact[],
) {
  const metadata =
    message.metadata && typeof message.metadata === "object"
      ? message.metadata
      : undefined;
  const rawArtifacts = metadata?.workspace_related_artifacts;
  const restoredArtifacts: Artifact[] = [];

  if (Array.isArray(rawArtifacts)) {
    rawArtifacts.forEach((item, artifactIndex) => {
      if (!item || typeof item !== "object") {
        return;
      }

      const value = item as Record<string, unknown>;
      const rawName = readHistoryArtifactText(value, "name");
      const rawPath = readHistoryArtifactText(value, "path");
      const rawKind = readHistoryArtifactText(value, "kind").toLowerCase();
      const rawLanguage = readHistoryArtifactText(value, "language");
      const rawSummary = readHistoryArtifactText(value, "summary");
      const basename =
        rawName ||
        rawPath.split("/").filter(Boolean).pop() ||
        "runtime-evidence.json";
      const path = rawPath || `runtime/${basename}`;

      if (rawKind === "image") {
        const resolvedContent =
          readHistoryArtifactText(value, "content", "src", "url") ||
          rawPath ||
          path;
        restoredArtifacts.push({
          id: buildHistoryArtifactId(sessionId, messageIndex, artifactIndex, basename),
          name: basename,
          path,
          summary:
            rawSummary ||
            readHistoryArtifactText(value, "revised_prompt", "revisedPrompt") ||
            "Recovered generated image from persisted session history.",
          kind: "image",
          content: resolvedContent,
          mimeType:
            readHistoryArtifactText(value, "mime_type", "mimeType") || "image/png",
          byteCount: readHistoryArtifactNumber(value, "byte_count", "byteCount"),
          sha256: readHistoryArtifactText(value, "sha256"),
          revisedPrompt: readHistoryArtifactText(
            value,
            "revised_prompt",
            "revisedPrompt",
          ),
        });
        return;
      }

      restoredArtifacts.push({
        id: buildHistoryArtifactId(sessionId, messageIndex, artifactIndex, basename),
        name: basename,
        path,
        summary:
          rawSummary || "Recovered runtime evidence from persisted session history.",
        kind: rawKind === "code" || rawKind === "html" ? rawKind : "json",
        language:
          rawLanguage === "tsx" ||
          rawLanguage === "ts" ||
          rawLanguage === "html"
            ? rawLanguage
            : "json",
        content: JSON.stringify(value.content ?? null, null, 2),
      });
    });
  }

  return [...restoredArtifacts, ...generatedImageArtifacts];
}

function buildHistoryArtifactId(
  sessionId: string,
  messageIndex: number,
  artifactIndex: number,
  basename: string,
) {
  return [
    "persisted-history",
    sessionId,
    messageIndex,
    artifactIndex,
    basename.replace(/[^a-zA-Z0-9]+/g, "-").replace(/^-+|-+$/g, "").toLowerCase(),
  ].join(":");
}

function readHistoryArtifactText(
  artifact: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = artifact[key];
    if (typeof value === "string") {
      const trimmed = value.trim();
      if (trimmed) {
        return trimmed;
      }
    }
  }
  return "";
}

function readHistoryArtifactNumber(
  artifact: Record<string, unknown>,
  ...keys: string[]
) {
  for (const key of keys) {
    const value = artifact[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
    if (typeof value === "string") {
      const parsed = Number(value);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return undefined;
}

function extractGeneratedImagesFromAssistantMessage(
  message: SessionHistoryMessage,
  sessionId: string,
) {
  return buildGeneratedImageAttachments(sessionId, message.metadata);
}

function buildGeneratedImageUrl(sessionId: string, name: string) {
  return buildRuntimeUrl(
    `/api/runtime/sessions/${encodeURIComponent(sessionId)}/generated-images/${encodeURIComponent(name)}`,
  );
}

function buildGeneratedImageArtifactId(sessionId: string, name: string) {
  return ["generated-image", sessionId, sanitizeArtifactToken(name || "generated-image")].join(":");
}

function sanitizeArtifactToken(value: string) {
  return value
    .trim()
    .replace(/[^a-zA-Z0-9_-]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .toLowerCase();
}

function stripFileExtension(value: string) {
  return value.replace(/\.[^.]+$/, "");
}

function filepathBase(value: string) {
  return value.split(/[\\/]/).filter(Boolean).pop() || "";
}

function truncateText(value: string, limit: number) {
  const normalized = value.trim().replace(/\s+/g, " ");
  if (normalized.length <= limit) {
    return normalized;
  }
  if (limit <= 3) {
    return normalized.slice(0, limit);
  }
  return `${normalized.slice(0, limit - 3)}...`;
}

function getHistoryMessageAuthor(role: string) {
  switch (role) {
    case "assistant":
      return "Runtime assistant";
    case "system":
      return "System context";
    case "tool":
      return "Tool receipt";
    default:
      return "You";
  }
}

function getPrimaryTextContent(message: ChatMessage) {
  return message.segments
    .filter(
      (segment): segment is Extract<MessageSegment, { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n\n")
    .trim();
}
