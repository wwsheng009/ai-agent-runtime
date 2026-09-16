// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type ChatMessage, type MessageSegment } from "@/data/mock";
import { type SessionHistoryMessage, type SessionHistoryToolCall } from "@/types/runtime";
import { parseToolDetailsFromArgsText, resolveToolSegmentDetails } from "@/lib/tool-row/details";

import { extractGeneratedImagesFromAssistantMessage } from "./generated-images";
import { buildHistoryArtifacts, normalizeSessionHistoryMessages } from "./history-artifacts";
import { mergeUniqueStrings } from "./shared";
import { getHistoryMessageAuthor, getPrimaryTextContent } from "./text-utils";

type HistoryMessageMapping = {
  artifacts: Artifact[];
  message: ChatMessage;
};

export function readFirstTextValue(
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

export function readFirstValue(
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

export function readFirstNumberValue(
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

export function createJsonArtifact(
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

/**
 * 与 `createJsonArtifact` 同形，但 `content` 只在真正被读取时（产物面板/详情弹窗
 * 渲染、导出）才序列化，并按产物实例记忆一次。
 *
 * 运行时事件产物（`session-runtime-events-*.json`）每来一个 SSE 事件就重建一次，
 * 而它承载的是最近 100 条事件的完整 payload：CDP CPU profile 实测
 * `createJsonArtifact` 单个回合 729ms（≈1ms × 734 个事件），且每次都产出几十 KB
 * 字符串再丢掉（GC 压力）。流式期间没有任何 UI 会读它，因此把序列化推迟到读取时
 * 即可，语义（`artifact.content` 的字符串内容）完全不变。
 */
export function createLazyJsonArtifact(
  id: string,
  filename: string,
  summary: string,
  buildPayload: () => unknown,
): Artifact {
  let cached: string | undefined;
  return {
    id,
    name: filename,
    path: `runtime/${filename}`,
    summary,
    kind: "json",
    language: "json",
    get content() {
      if (cached === undefined) {
        cached = JSON.stringify(buildPayload(), null, 2);
      }
      return cached;
    },
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

const HISTORY_REASONING_METADATA_KEY = "reasoning_details";

/**
 * 从历史消息 metadata 中提取可展示的 reasoning 文本。
 * 对齐后端 ReasoningBlock.DisplayText 语义：
 * - visibility="none"/"opaque" 时跳过；
 * - 优先 summary，其次 content。
 */
function extractHistoryReasoningText(
  metadata: Record<string, unknown> | undefined,
): string {
  if (!metadata) return "";
  const raw = metadata[HISTORY_REASONING_METADATA_KEY];
  if (!raw || typeof raw !== "object") return "";
  const block = raw as Record<string, unknown>;
  const visibility =
    typeof block.visibility === "string" ? block.visibility.trim() : "";
  if (visibility === "none" || visibility === "opaque") return "";
  if (typeof block.summary === "string" && block.summary.trim()) {
    return block.summary.trim();
  }
  if (typeof block.content === "string" && block.content.trim()) {
    return block.content.trim();
  }
  return "";
}

/** 历史工具结果在展开面板里的正文上限（超长结果只截断展示，不改写数据）。 */
export const HISTORY_TOOL_RESULT_LIMIT = 4000;

/**
 * 工具调用索引：assistant 消息上的 `tool_calls` → `tool_call_id` 可回填名称与入参。
 * 历史里的 tool 回执只带 `tool_call_id`，没有这条索引就只能回退成通用文本行。
 */
export function indexHistoryToolCalls(
  history: readonly SessionHistoryMessage[],
): Map<string, SessionHistoryToolCall> {
  const index = new Map<string, SessionHistoryToolCall>();
  for (const message of history) {
    for (const call of message.tool_calls ?? []) {
      const id = typeof call?.id === "string" ? call.id.trim() : "";
      if (id && !index.has(id)) {
        index.set(id, call);
      }
    }
  }
  return index;
}

/** 配对调用 → 入参文本：优先 RawInput，其次 arguments 的 JSON 串。 */
function historyToolArgsText(call: SessionHistoryToolCall | undefined): string {
  if (!call) {
    return "";
  }
  if (typeof call.input === "string" && call.input.trim()) {
    return call.input.trim();
  }
  if (call.arguments && typeof call.arguments === "object") {
    try {
      return JSON.stringify(call.arguments);
    } catch {
      return "";
    }
  }
  return "";
}

/**
 * 历史 tool 回执行 → tool segment（B4/B5：折叠态 24px 单行的数据前提）。
 * 名称/入参优先取配对 `tool_calls`，缺失退回 metadata；明细复用 argsSummary 解析。
 */
function buildHistoryToolSegment(
  message: SessionHistoryMessage,
  call: SessionHistoryToolCall | undefined,
): MessageSegment {
  const metadata =
    message.metadata && typeof message.metadata === "object"
      ? (message.metadata as Record<string, unknown>)
      : {};
  const name =
    (typeof call?.name === "string" ? call.name.trim() : "") ||
    readFirstTextValue(metadata, "tool_name", "toolName", "name") ||
    "tool";
  const argsSummary = historyToolArgsText(call);
  const errorMessage = readFirstTextValue(
    metadata,
    "error",
    "error_message",
    "errorMessage",
  );
  const result =
    typeof message.content === "string" ? message.content.trim() : "";
  const toolCallId =
    (typeof message.tool_call_id === "string"
      ? message.tool_call_id.trim()
      : "") || (typeof call?.id === "string" ? call.id.trim() : "");

  const segment: Extract<MessageSegment, { type: "tool" }> = {
    type: "tool",
    name,
    status: errorMessage ? "error" : "finished",
  };
  if (toolCallId) {
    segment.toolCallId = toolCallId;
  }
  if (argsSummary) {
    segment.argsSummary = argsSummary;
  }
  if (result) {
    segment.resultSummary =
      result.length > HISTORY_TOOL_RESULT_LIMIT
        ? `${result.slice(0, HISTORY_TOOL_RESULT_LIMIT).trimEnd()}…`
        : result;
  }
  if (errorMessage) {
    segment.errorMessage = errorMessage;
  }
  // 历史结果正文（未截断）里可能带 ```diff 围栏：行级 diff 视图优先用它恢复真实补丁文本。
  const details =
    parseToolDetailsFromArgsText(argsSummary, name, result) ??
    (result ? resolveToolSegmentDetails({ name, resultSummary: result }) : undefined);
  if (details) {
    segment.details = details;
  }
  return segment;
}

function buildHistoryMessage(
  sessionId: string,
  index: number,
  message: SessionHistoryMessage,
  artifacts: Artifact[],
  generatedImageSegments: MessageSegment[],
  toolCalls: ReadonlyMap<string, SessionHistoryToolCall>,
): ChatMessage {
  const relatedArtifactIds = artifacts.map((artifact) => artifact.id);
  const stableId = readHistoryMessageIdentity(message);
  const reasoningText = extractHistoryReasoningText(message.metadata);
  const toolCallId =
    typeof message.tool_call_id === "string" ? message.tool_call_id.trim() : "";
  // 工具回执：历史里 role="tool" 独立成条，按其配对调用还原 tool segment，
  // 由消息列表按 24px 工具行呈现（不再是通用「上下文注入」行）。
  // 空消息不占位（§12.1.4）：工具回合 / 仅推理 / 仅附件的 assistant 消息 content
  // 为空是正常协议形态，不能降级成 "[empty message]" 文本段顶到过程区上屏。
  const contentText = message.content?.trim() ?? "";
  const segments: MessageSegment[] =
    message.role === "tool"
      ? [
          buildHistoryToolSegment(
            message,
            toolCallId ? toolCalls.get(toolCallId) : undefined,
          ),
        ]
      : [
          // 推理先于正文落位：思考过程在上、正式回答在下。历史条目只保存最终正文
          // 与合并后的推理块（没有逐帧顺序信息），恢复时按固定顺序还原；
          // 旧实现把正文放前，页面就成了「先正文、后推理过程」。
          ...(reasoningText
            ? [{ type: "reasoning" as const, content: reasoningText }]
            : []),
          ...(contentText
            ? [{ type: "text" as const, content: contentText }]
            : []),
          ...generatedImageSegments,
        ];
  return {
    id: stableId || `${sessionId}-history-${index}`,
    role: message.role === "user" ? "user" : "assistant",
    author: getHistoryMessageAuthor(message.role),
    label: message.role || "runtime",
    relatedArtifactIds:
      relatedArtifactIds.length > 0 ? relatedArtifactIds : undefined,
    segments,
  };
}

export function mapSessionHistoryToMessages(
  sessionId: string,
  history: SessionHistoryMessage[] | null | undefined,
  existingMessages: ChatMessage[],
) {
  const usedMessageIds = new Set<string>();
  const normalizedHistory = normalizeSessionHistoryMessages(history);
  const toolCalls = indexHistoryToolCalls(normalizedHistory);

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
      toolCalls,
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
      // 文本兜底只在历史条目确有正文时生效：工具回执没有文本段（primary text 为空），
      // 否则会被误并进任意同角色的空文本消息、从时间线上消失。
      if (!fallbackText) {
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
    if (stableId) {
      usedMessageIds.add(stableId);
    }
    // 代码段透传；live 工具卡在历史里可能没有对应表达（工具回执的 tool segment
    // 现在由 fallback 自带给历史条目），合并时按 toolCallId 去重，避免同一调用双份。
    const fallbackToolCallIds = new Set(
      fallback.segments
        .filter(
          (
            segment,
          ): segment is Extract<MessageSegment, { type: "tool" }> =>
            segment.type === "tool",
        )
        .map((segment) => segment.toolCallId)
        .filter((id): id is string => Boolean(id)),
    );
    const preservedSegments = matched.segments.filter((segment) => {
      if (segment.type === "code") {
        return true;
      }
      if (segment.type !== "tool") {
        return false;
      }
      return !segment.toolCallId || !fallbackToolCallIds.has(segment.toolCallId);
    });
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
        segments: [...fallback.segments, ...preservedSegments],
      },
    } satisfies HistoryMessageMapping;
  });
}
