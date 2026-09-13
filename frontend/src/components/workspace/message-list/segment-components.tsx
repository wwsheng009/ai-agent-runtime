// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 组件专用模块：本文件只导出组件，满足 react-refresh/only-export-components。

import { lazy } from "react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { useTypewriter } from "@/hooks/workspace/use-typewriter";
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
  const typedContent = useTypewriter(content, streaming === true);
  return (
    <MessageMarkdown
      content={typedContent}
      interrupted={interrupted}
      streaming={streaming}
    />
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
