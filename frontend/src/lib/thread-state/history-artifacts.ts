// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import { normalizeSessionId } from "@/lib/session-id";
import { type SessionHistoryMessage, type SessionHistoryResponse, type SessionRuntimeEvent } from "@/types/runtime";

import {
  createJsonArtifact,
  createLazyJsonArtifact,
  mapSessionHistoryToMessages,
} from "./history-mapping";
import { getRuntimeEventSeq } from "./sessions";
import { upsertArtifacts } from "./shared";

export const MAX_RUNTIME_EVENTS = 100;

/** 会话历史页 → 线程投影的公共前置：会话 id 归一 + 历史产物 + 消息映射。 */
function mapHistoryPage(thread: Thread, response: SessionHistoryResponse) {
  const sessionId =
    normalizeSessionId(response.session_id) ||
    normalizeSessionId(thread.sessionId);
  return {
    sessionId,
    historyArtifact: buildSessionHistoryArtifact(response),
    mappedHistory: mapSessionHistoryToMessages(
      sessionId,
      response.history,
      thread.messages,
    ),
  };
}

export function applySessionHistoryToThread(
  thread: Thread,
  response: SessionHistoryResponse,
) {
  const transport: Thread["transport"] =
    thread.transport === "error" ? "error" : "live";
  const { sessionId, historyArtifact, mappedHistory } = mapHistoryPage(
    thread,
    response,
  );
  const mappedMessages = mappedHistory.map((item) => item.message);
  // 已加载的更早内容（「加载更早」前插的常住窗口）：本页首条消息**之前**的本地
  // 消息不在本页返回范围内，但仍是服务端确认过的历史前缀。重同步（挂载 / 回合
  // 结束 / 回滚事件）若整体覆盖，用户刚翻出来的旧消息会凭空消失——因此按「本页
  // 首条消息在本地列表中的位置」保留它前面的部分；本页为空（截断到 0 条）时
  // 找不到锚点，自然回落成整体替换。
  const pageMessageIds = new Set(mappedMessages.map((message) => message.id));
  const firstPageIndex = thread.messages.findIndex((message) =>
    pageMessageIds.has(message.id),
  );
  const olderResidentMessages =
    firstPageIndex > 0 ? thread.messages.slice(0, firstPageIndex) : [];
  // 历史是权威投影，但在途/被中断的本地消息尚未（或不会）落盘：用户按 Ctrl+Enter
  // 中止流式回合时后端只持久化了用户消息，若此时用历史整体覆盖，刚渲染出的部分
  // 回答会连同“已停止”标记一起消失。这里保留历史未覆盖到的 live-only 消息，
  // 追加在权威历史之后（它们必然是最新的回合）。
  const liveOnlyMessages = thread.messages.filter(
    (message) =>
      (message.streaming === true || message.interrupted === true) &&
      !mappedMessages.some((mapped) => mapped.id === message.id),
  );
  // `history: null` 与 `history: []` 是两种语义，不能都当成「没有权威历史」：
  // - null：本次没有拿到权威历史（如恢复流程降级），保留本地消息；
  // - [] ：服务端确认该会话**没有**任何消息——回溯/还原把历史截断到 0 条时正是
  //   这个形态（后端返回 `{"count":0,"history":[]}`）。若沿用「保留本地消息」，
  //   消息列表会停留在回滚前的内容，用户看到的就是「点了确认没反应」。
  const resolvedMessages = Array.isArray(response.history)
    ? [...olderResidentMessages, ...mappedMessages, ...liveOnlyMessages]
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

/**
 * 更早一页的**前插**（对话面「加载更早」）：`before_seq` 页映射成消息后放到列表
 * 最前，与本地已有消息按 id 去重——重复请求同一页（或与常住窗口重叠）时不会出现
 * 双份消息；整页都已在本地时返回原线程，避免无意义的重渲染与滚动对账。
 */
export function prependSessionHistoryToThread(
  thread: Thread,
  response: SessionHistoryResponse,
) {
  const { sessionId, historyArtifact, mappedHistory } = mapHistoryPage(
    thread,
    response,
  );
  const existingIds = new Set(thread.messages.map((message) => message.id));
  const olderMessages = mappedHistory
    .map((item) => item.message)
    .filter((message) => !existingIds.has(message.id));
  if (olderMessages.length === 0) {
    return thread;
  }
  return {
    ...thread,
    updatedAt: new Date().toISOString(),
    sessionId,
    messages: [...olderMessages, ...thread.messages],
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
  // 惰性产物：每来一个事件都会重建本产物，但内容（最近 100 条事件的完整
  // payload）只有面板/弹窗真的渲染时才需要（见 createLazyJsonArtifact）。
  return createLazyJsonArtifact(
    `session-runtime-events-${sessionId}`,
    `session-runtime-events-${sessionId}.json`,
    "Runtime events streamed from /api/runtime/sessions/{id}/runtime/stream.",
    () => ({
      session_id: sessionId,
      count: events.length,
      events,
    }),
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
