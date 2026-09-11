// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type ChatMessage, type MessageSegment } from "@/data/mock";

import { buildStreamingMessageSegments } from "./events";
import { type GeneratedImageAttachments, isGeneratedImageSegment, upsertGeneratedImageSegment } from "./generated-images";
import { upsertToolSegment } from "./tools";

export const STREAM_PLACEHOLDER_TEXT = "...";

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
