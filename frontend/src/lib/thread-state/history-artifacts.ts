// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import { normalizeSessionId } from "@/lib/session-id";
import { type SessionHistoryMessage, type SessionHistoryResponse, type SessionRuntimeEvent } from "@/types/runtime";

import { createJsonArtifact, mapSessionHistoryToMessages } from "./history-mapping";
import { getRuntimeEventSeq } from "./sessions";
import { upsertArtifacts } from "./shared";

export const MAX_RUNTIME_EVENTS = 100;

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
  const mappedMessages = mappedHistory.map((item) => item.message);
  // 历史是权威投影，但在途/被中断的本地消息尚未（或不会）落盘：用户按 Ctrl+Enter
  // 中止流式回合时后端只持久化了用户消息，若此时用历史整体覆盖，刚渲染出的部分
  // 回答会连同“已停止”标记一起消失。这里保留历史未覆盖到的 live-only 消息，
  // 追加在权威历史之后（它们必然是最新的回合）。
  const liveOnlyMessages = thread.messages.filter(
    (message) =>
      (message.streaming === true || message.interrupted === true) &&
      !mappedMessages.some((mapped) => mapped.id === message.id),
  );
  const resolvedMessages =
    mappedHistory.length > 0
      ? [...mappedMessages, ...liveOnlyMessages]
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

export function buildRuntimeEventKey(event: SessionRuntimeEvent) {
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

function buildSessionHistoryArtifact(response: SessionHistoryResponse) {
  return createJsonArtifact(
    `session-history-${response.session_id}`,
    `session-history-${response.session_id}.json`,
    "Authoritative session history loaded from /api/runtime/sessions/{id}/history.",
    response,
  );
}

export function buildSessionRuntimeEventsArtifact(
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

export function normalizeSessionHistoryMessages(
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
        // 工具配对字段必须透传：tool 回执只有带着 tool_call_id，才能回填工具名/入参，
        // 进而按 24px 工具行呈现（否则只能退化成通用文本行）。
        ...(Array.isArray(item.tool_calls)
          ? { tool_calls: item.tool_calls }
          : {}),
        ...(typeof item.tool_call_id === "string" && item.tool_call_id.trim()
          ? { tool_call_id: item.tool_call_id }
          : {}),
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

export function buildHistoryArtifacts(
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
