/**
 * §12.1.4：消息级空行收敛 —— 判断一条消息是否还会产出任何可见行。
 *
 * 背景：消息列表是 `flex flex-col gap-4`，**任何** `<article>` 都是一个 flex item，
 * 即便内部一个可见子节点都没有（流式首块到达前的助手空壳 / 纯工具回合），
 * 也会在相邻消息之间占掉一条 16px 的空白行。参考站的做法是在节点级收口
 * （`AssistantMarkdown` 的 `hasVisible` 门：只有工具调用头、没有可见块时 `return null`），
 * 本地对应到「要不要产出这条消息的 article」。
 *
 * 判定只覆盖**渲染层可判定**的内容：段落可见性、关联产物、回合用量。
 * 与 `visible-text.ts` 同一口径，避免各组件各写一份 `trim()`。
 */
import { type ChatMessage, type MessageSegment } from "@/data/mock";

import { hasVisibleText } from "./visible-text";

/** 单个段落是否会被渲染成可见行（空文本 / 空推理不算）。 */
export function segmentHasVisibleContent(segment: MessageSegment): boolean {
  switch (segment.type) {
    case "text":
    case "reasoning":
      return hasVisibleText(segment.content);
    case "callout":
      return hasVisibleText(segment.title) || hasVisibleText(segment.content);
    case "code":
      return hasVisibleText(segment.code);
    case "checklist":
    case "receipt":
      return hasVisibleText(segment.title) || segment.items.length > 0;
    case "image":
      return hasVisibleText(segment.src) || hasVisibleText(segment.caption);
    case "image-placeholder":
    case "tool":
      // 工具行 / 图片占位行自带状态信息，永远算可见（参考站的工具头同理）。
      return true;
    default:
      return true;
  }
}

/**
 * 消息是否有可见内容。
 *
 * @param message 待判定消息。
 * @param relatedArtifactCount 命中的关联产物条数（产物区会独立成行）。
 */
export function hasVisibleMessageContent(
  message: ChatMessage,
  relatedArtifactCount = 0,
): boolean {
  if (relatedArtifactCount > 0) {
    return true;
  }
  // 回合用量承载 turn-tail 行，即便正文为空也要保留该行。
  if (message.usage != null) {
    return true;
  }
  return message.segments.some(segmentHasVisibleContent);
}
