/**
 * P1-1：MessageSegment → ChatViewNode 的机械映射。
 *
 * 只做分类与 key 生成，不改变 segment 语义；渲染层仍消费原始 segment。
 */
import type { MessageSegment } from "@/data/mock";

import type { ChatViewNode, ChatViewNodeKind } from "./types";

/** segment 类别 → 节点类别。 */
export function segmentKind(segment: MessageSegment): ChatViewNodeKind {
  if (segment.type === "text") return "text";
  if (segment.type === "reasoning") return "reasoning";
  if (segment.type === "tool") return "tool";
  if (segment.type === "callout") return "system";
  return "rich";
}

/** 证据原始 id（工具 callId / 图片 imageId）。 */
export function segmentId(segment: MessageSegment): string | undefined {
  if (segment.type === "tool") return segment.toolCallId;
  if (segment.type === "image") return segment.imageId;
  if (segment.type === "image-placeholder") return segment.imageId;
  return undefined;
}

/** 关联跳转目标（当前仅有图片 → artifact）。 */
export function segmentTarget(segment: MessageSegment): string | undefined {
  if (segment.type === "image" && segment.artifactId) return segment.artifactId;
  return undefined;
}

/**
 * 过程证据：Turn 结束后可折叠。
 * 推理、工具调用与运行提示（callout）都属于「过程」，最终回答正文与富内容不属于。
 */
export function isEvidenceSegment(segment: MessageSegment): boolean {
  return (
    segment.type === "reasoning" ||
    segment.type === "tool" ||
    segment.type === "callout"
  );
}

/** 最终回答可承载类别：非空文本 / 图片。 */
export function isAnswerSegment(segment: MessageSegment): boolean {
  if (segment.type === "text") return segment.content.trim().length > 0;
  return segment.type === "image";
}

/**
 * 生成稳定节点序列。
 * key 规则：`<messageId>:<kind>:<callId | imageId | index>`；
 * 相同 message 的前缀段在任何追加场景下 key 不变。
 */
export function buildNodes(
  messageId: string,
  segments: MessageSegment[],
): ChatViewNode[] {
  return segments.map((segment, index) => {
    const kind = segmentKind(segment);
    const identity = segmentId(segment) ?? String(index);
    return {
      key: `${messageId}:${kind}:${identity}`,
      kind,
      id: segmentId(segment),
      target: segmentTarget(segment),
      evidence: isEvidenceSegment(segment),
      segment,
    };
  });
}
