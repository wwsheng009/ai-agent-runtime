/**
 * P1-1：会话视图投影入口（Definition / Node）。
 *
 * 输入一条 ChatMessage（可选流式/展开/子代理计数），输出：
 * - 可见节点与隐藏的过程证据节点；
 * - 折叠标志与结构化摘要（不含文案）；
 * - system/message 的 System prompt 行语义。
 */
import type { ChatMessage } from "@/data/mock";

import { findFinalAnswerStart, summarizeCollapsedEvidence } from "./collapse";
import { buildNodes } from "./nodes";
import type {
  ChatViewProjection,
  ProjectChatViewOptions,
} from "./types";

/** system/message：请求前折叠的 System prompt 行（历史上下文消息）。 */
export function isSystemPromptMessage(
  message: Pick<ChatMessage, "label" | "author">,
): boolean {
  return message.label === "system" || message.author === "System context";
}

export function projectChatView(
  message: ChatMessage,
  options: ProjectChatViewOptions = {},
): ChatViewProjection {
  const allNodes = buildNodes(message.id, message.segments);

  if (isSystemPromptMessage(message)) {
    const collapsed = options.expanded !== true;
    return {
      nodes: collapsed ? [] : allNodes,
      hiddenNodes: collapsed ? allNodes : [],
      collapsed,
      summary: null,
      finalAnswerStart: -1,
      systemPrompt: true,
    };
  }

  const finalAnswerStart = findFinalAnswerStart(allNodes);
  const canCollapse =
    options.streaming !== true &&
    options.expanded !== true &&
    finalAnswerStart > 0;

  if (!canCollapse) {
    return {
      nodes: allNodes,
      hiddenNodes: [],
      collapsed: false,
      summary: null,
      finalAnswerStart,
      systemPrompt: false,
    };
  }

  const hiddenNodes = allNodes.slice(0, finalAnswerStart);
  return {
    nodes: allNodes.slice(finalAnswerStart),
    hiddenNodes,
    collapsed: true,
    summary: summarizeCollapsedEvidence(hiddenNodes, {
      subagents: options.subagentCount,
    }),
    finalAnswerStart,
    systemPrompt: false,
  };
}
