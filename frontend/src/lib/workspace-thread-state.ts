import {
  type Artifact,
  type ChatMessage,
  type MessageSegment,
  type Thread,
} from "@/data/mock";
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
    const restoredArtifacts = buildHistoryArtifacts(sessionId, index, item);
    const fallback = buildHistoryMessage(
      sessionId,
      index,
      item,
      restoredArtifacts,
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
    const relatedArtifactIds =
      matched.relatedArtifactIds && matched.relatedArtifactIds.length > 0
        ? matched.relatedArtifactIds
        : fallback.relatedArtifactIds;

    return {
      artifacts:
        matched.relatedArtifactIds && matched.relatedArtifactIds.length > 0
          ? []
          : restoredArtifacts,
      message: {
        ...matched,
        role: fallback.role,
        author: matched.author || fallback.author,
        label: matched.label || fallback.label,
        relatedArtifactIds,
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
  return {
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
) {
  const metadata =
    message.metadata && typeof message.metadata === "object"
      ? message.metadata
      : undefined;
  const rawArtifacts = metadata?.workspace_related_artifacts;
  if (!Array.isArray(rawArtifacts)) {
    return [] as Artifact[];
  }

  return rawArtifacts.flatMap((item, artifactIndex) => {
    if (!item || typeof item !== "object") {
      return [];
    }

    const value = item as Record<string, unknown>;
    const rawName = readHistoryArtifactText(value, "name");
    const rawPath = readHistoryArtifactText(value, "path");
    const rawKind = readHistoryArtifactText(value, "kind");
    const rawLanguage = readHistoryArtifactText(value, "language");
    const rawSummary = readHistoryArtifactText(value, "summary");
    const basename =
      rawName || rawPath.split("/").filter(Boolean).pop() || "runtime-evidence.json";
    const path = rawPath || `runtime/${basename}`;

    return [
      {
        id: buildHistoryArtifactId(sessionId, messageIndex, artifactIndex, basename),
        name: basename,
        path,
        summary: rawSummary || "Recovered runtime evidence from persisted session history.",
        kind: rawKind === "code" || rawKind === "html" ? rawKind : "json",
        language:
          rawLanguage === "tsx" ||
          rawLanguage === "ts" ||
          rawLanguage === "html"
            ? rawLanguage
            : "json",
        content: JSON.stringify(value.content ?? null, null, 2),
      } satisfies Artifact,
    ];
  });
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
  key: string,
) {
  const value = artifact[key];
  return typeof value === "string" ? value.trim() : "";
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
