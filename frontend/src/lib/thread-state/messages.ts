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

/**
 * 结束当前仍在跑的推理段（`running: true` → `false`）。
 *
 * 推理行只有在 `running` 为真时才显示「推理中…」与转圈（见
 * components/workspace/message-reasoning-row.tsx）。此前这个标记只会被
 * 「整条消息按最终快照重建」清掉，于是模型已经进入工具/正文阶段、推理块早就
 * 写完了，行上仍挂着运行态——观感就是「页面一直在推理」。工具帧与首个正文
 * 分片到达时调用本函数，让渲染层及时切回已完成的推理行。
 *
 * 无段可改时返回**原数组引用**：runtime 事件按帧触发（单回合上千帧），
 * 每次分配新数组会让下游 memo 全部失效。
 */
export function closeRunningReasoningSegments(segments: MessageSegment[]) {
  let changed = false;
  const nextSegments = segments.map((segment) => {
    if (segment.type !== "reasoning" || segment.running !== true) {
      return segment;
    }
    changed = true;
    return { ...segment, running: false };
  });
  return changed ? nextSegments : segments;
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
