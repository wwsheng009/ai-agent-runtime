/**
 * P1-1：折叠谓词与摘要（纯函数，无 React / 无 i18n 文案）。
 *
 * 折叠语义：
 * - Turn 结束后折叠过程证据，保留最终回答；
 * - final-answer 边界 = 自尾向前的连续非证据块，且该块含非空文本/图片；
 * - 无 final answer（末尾仍是工具/推理）的 Turn 不折叠，过程保持可见。
 */
import type { MessageSegment } from "@/data/mock";

import { isAnswerSegment } from "./nodes";
import type {
  ChatViewNode,
  CollapsedSummaryPart,
  CollapsedTurnSummary,
} from "./types";

/**
 * 最终回答起始下标（相对全量节点）；无最终回答返回 -1。
 *
 * 自尾向前收集「非过程证据」的连续块；块内必须至少含一个非空文本或图片，
 * 否则视为无最终回答（例如末尾是工具调用、推理或纯富内容）。
 */
export function findFinalAnswerStart(nodes: ChatViewNode[]): number {
  let start = nodes.length;
  while (start > 0 && !nodes[start - 1].evidence) {
    start -= 1;
  }
  const block = nodes.slice(start);
  if (!block.some((node) => isAnswerSegment(node.segment))) return -1;
  return start;
}

/** 工具是否带回复：有结果摘要或错误回执。 */
function toolHasReply(segment: MessageSegment): boolean {
  if (segment.type !== "tool") return false;
  const result = segment.resultSummary?.trim() ?? "";
  const error = segment.errorMessage?.trim() ?? "";
  return result.length > 0 || error.length > 0;
}

/**
 * 折叠摘要：工具数 / 带回复消息数 / 子代理数。
 * 零值段省略；全零时 empty = true（渲染层显示统一占位，如 "Thought for a while"）。
 */
export function summarizeCollapsedEvidence(
  nodes: ChatViewNode[],
  extras: { subagents?: number } = {},
): CollapsedTurnSummary {
  const toolNodes = nodes.filter((node) => node.kind === "tool");
  const tools = toolNodes.length;
  const replies = toolNodes.filter((node) => toolHasReply(node.segment)).length;
  const subagents = extras.subagents ?? 0;

  const parts: CollapsedSummaryPart[] = [];
  if (tools > 0) parts.push({ kind: "tools", count: tools });
  if (replies > 0) parts.push({ kind: "replies", count: replies });
  if (subagents > 0) parts.push({ kind: "subagents", count: subagents });

  return {
    tools,
    replies,
    subagents,
    parts,
    empty: parts.length === 0,
  };
}
