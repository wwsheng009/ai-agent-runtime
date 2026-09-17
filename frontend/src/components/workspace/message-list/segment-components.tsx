// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 组件专用模块：本文件只导出组件，满足 react-refresh/only-export-components。

import { lazy } from "react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { type MessageSegment } from "@/data/mock";
import { useLiveStreamTextRef } from "@/hooks/workspace/use-live-stream-text-ref";
import { useTypewriter } from "@/hooks/workspace/use-typewriter";

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
  liveStreamId,
  onSelectArtifact,
  streaming,
}: {
  content: string;
  interrupted?: boolean;
  /** live 通道键（消息 id）：非流式行传 null，避免无谓订阅。 */
  liveStreamId?: string | null;
  /** C5：透传给 MessageMarkdown，供 tool_result 里的 artifact 指针行打开详情对话框。 */
  onSelectArtifact?: (artifactId: string) => void;
  streaming?: boolean;
}) {
  // live 通道（见 lib/live-stream-text.ts）：流式期间正文按增量写外部 store，
  // 取代「每次增量改页面级 thread state」（后者会连带 topbar / 侧栏 / composer /
  // 消息列整棵树重渲染，实测占流式期主线程开销约 2/3）。
  //
  // 这里只订阅 ref、不订阅渲染：增量只在下一帧被打字机顺带揭示。若在增量到达时
  // 也渲染一次，就会与打字机的揭示节拍各驱动一遍同一条消息的 markdown 尾块解析
  // （实测 ScriptDur 1.95s vs 单驱动 1.05s、LayoutCount 442 vs 201）。
  // 目标文本的选取（含「live 短于 store 副本时回落」的自愈守卫）在 useTypewriter 内，
  // 那里同时能看到 store 副本与 live ref。
  const liveTextRef = useLiveStreamTextRef(
    streaming ? (liveStreamId ?? null) : null,
  );
  // 打字机（批次 D3 曾因「滞后帧 → 冻结前缀比对判成改写 → generation++ → 冻结块
  // remount」删除，本批次以「单调揭示」重建）：`useTypewriter` 只做两件事——
  // ① 单调追加：揭示量只增不减，冻结前缀比对永远判「追加」；
  // ② 只对正在增长的目标文本生效：挂载即完整（历史回放 / 历史同步重建）与
  //    同一条流式消息里更早的静态文本段都不打字，直接直挂。
  const revealedContent = useTypewriter(
    content,
    Boolean(streaming) && !interrupted,
    liveTextRef,
  );
  return (
    <MessageMarkdown
      content={revealedContent}
      interrupted={interrupted}
      onSelectArtifact={onSelectArtifact}
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
