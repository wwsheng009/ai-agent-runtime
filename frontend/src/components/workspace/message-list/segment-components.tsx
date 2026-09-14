// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 组件专用模块：本文件只导出组件，满足 react-refresh/only-export-components。

import { lazy } from "react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { type MessageSegment } from "@/data/mock";

export const MessageRichSegment = lazy(() =>
  import("@/components/workspace/message-rich-content").then((module) => ({
    default: module.MessageRichSegment,
  })),
);
export const MessageRelatedArtifacts = lazy(() =>
  import("@/components/workspace/message-rich-content").then((module) => ({
    default: module.MessageRelatedArtifacts,
  })),
);

export function StreamingMarkdown({
  content,
  interrupted,
  streaming,
}: {
  content: string;
  interrupted?: boolean;
  streaming?: boolean;
}) {
  // 打字机已移除（方案 §8.6 / 批次 D3）：`useTypewriter` 返回的是
  // `content.slice(0, shown)`，滞后帧会让 MessageMarkdown 的冻结前缀比对
  // （stableContent.startsWith(上一帧)）判成「内容被改写」→ generation++ →
  // 已冻结块 remount 重解析。这里直连，内容按到达节奏渲染。
  return (
    <MessageMarkdown content={content} interrupted={interrupted} streaming={streaming} />
  );
}

export function MessageSegmentFallback({
  segment,
}: {
  segment: Exclude<MessageSegment, { type: "text" }>;
}) {
  const { t } = useTranslation("workspace");
  const label =
    segment.type === "code"
      ? "代码块"
      : segment.type === "image"
        ? "图片"
        : segment.type === "image-placeholder"
          ? "图片生成占位"
          : segment.type === "reasoning"
            ? "推理过程"
            : segment.type === "tool"
              ? "工具调用"
              : segment.title;
  return (
    <div
      aria-atomic="true"
      aria-live="polite"
      className="rounded-card border border-border bg-surface-softer px-3 py-3 text-sm text-muted-foreground"
      role="status"
    >
      {t("panels.messages.segmentFallback.loading", { label })}
    </div>
  );
}

export function RelatedArtifactsFallback({ count }: { count: number }) {
  const { t } = useTranslation("workspace");
  return (
    <div
      aria-atomic="true"
      aria-live="polite"
      className="mt-3 rounded-card border border-border bg-surface-softer px-3 py-3 text-sm text-muted-foreground"
      role="status"
    >
      {t("panels.messages.relatedArtifactsFallback.loading", { count })}
    </div>
  );
}
