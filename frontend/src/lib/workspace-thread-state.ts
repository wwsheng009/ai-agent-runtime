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

type AssistantMessageSegmentOptions = {
  status?: "streaming" | "stopped";
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
  });

  for (const segment of options?.existingSegments ?? []) {
    if (isGeneratedImageSegment(segment)) {
      segments = upsertGeneratedImageSegment(segments, segment);
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

function buildHistoryMessage(
  sessionId: string,
  index: number,
  message: SessionHistoryMessage,
  artifacts: Artifact[],
  generatedImageSegments: MessageSegment[],
): ChatMessage {
  const relatedArtifactIds = artifacts.map((artifact) => artifact.id);
  return {
    id: `${sessionId}-history-${index}`,
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

    const matched = existingMessages.find((message) => {
      if (usedMessageIds.has(message.id)) {
        return false;
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
  let nextThread = {
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

export function applySessionHistoryToThread(
  thread: Thread,
  response: SessionHistoryResponse,
) {
  const transport: Thread["transport"] =
    thread.transport === "error" ? "error" : "live";
  const historyArtifact = buildSessionHistoryArtifact(response);
  const mappedHistory = mapSessionHistoryToMessages(
    response.session_id,
    response.history,
    thread.messages,
  );
  return {
    ...thread,
    updatedAt: new Date().toISOString(),
    sessionId: response.session_id,
    transport,
    lastError: thread.transport === "error" ? thread.lastError : null,
    messages: mappedHistory.map((item) => item.message),
    artifacts: upsertArtifacts(thread.artifacts, [
      historyArtifact,
      ...mappedHistory.flatMap((item) => item.artifacts),
    ]),
  };
}

export function buildStreamingMessageSegments(
  text: string,
  source: string,
  reasoning: string,
  options?: {
    status?: "streaming" | "stopped";
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
      type: "code",
      language: "json",
      title: "Reasoning snapshot",
      code: JSON.stringify(
        {
          source,
          reasoning: reasoning.trim(),
        },
        null,
        2,
      ),
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

export function mergeRuntimeSessionsIntoThreads(
  threads: Thread[],
  sessions: RuntimeSessionRecord[],
) {
  const nextThreads = [...threads];
  let changed = false;

  for (const session of sessions) {
    const existingIndex = nextThreads.findIndex(
      (thread) => thread.sessionId === session.id || thread.id === session.id,
    );
    const title =
      session.metadata?.title?.trim() || `Runtime session ${session.id.slice(0, 10)}`;
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
        id: session.id,
        title,
        summary,
        updatedAt,
        status: mapSessionStateToThreadStatus(session.state),
        sessionId: session.id,
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
      id: current.sessionId ? current.id : session.id,
      title,
      summary,
      updatedAt,
      status: mapSessionStateToThreadStatus(session.state),
      sessionId: session.id,
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
) {
  return {
    id: messageId,
    role: "assistant" as const,
    author: "Runtime stream",
    label: "streaming",
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
  const eventKey = buildRuntimeEventKey(nextEvent);
  if (existingEvents.some((event) => buildRuntimeEventKey(event) === eventKey)) {
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
) {
  for (let index = thread.messages.length - 1; index >= 0; index--) {
    const message = thread.messages[index];
    if (message.role !== "assistant") {
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
  return [
    getRuntimeEventSeq(event),
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
