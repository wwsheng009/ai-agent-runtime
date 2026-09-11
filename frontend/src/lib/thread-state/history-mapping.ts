// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type ChatMessage, type MessageSegment } from "@/data/mock";
import { type SessionHistoryMessage } from "@/types/runtime";

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

function buildHistoryMessage(
  sessionId: string,
  index: number,
  message: SessionHistoryMessage,
  artifacts: Artifact[],
  generatedImageSegments: MessageSegment[],
): ChatMessage {
  const relatedArtifactIds = artifacts.map((artifact) => artifact.id);
  const stableId = readHistoryMessageIdentity(message);
  const reasoningText = extractHistoryReasoningText(message.metadata);
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
      ...(reasoningText
        ? [{ type: "reasoning" as const, content: reasoningText }]
        : []),
      ...generatedImageSegments,
    ],
  };
}

export function mapSessionHistoryToMessages(
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
