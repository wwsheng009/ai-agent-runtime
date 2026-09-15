// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 入口导出面保持不变（MessageMarkdown）；内部实现见 ./message-markdown/ 各域模块。

import { useDeferredValue, memo, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { SquareIcon } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { CodeBlock } from "@/components/ui/code-block";
import {
  normalizeMarkdown,
  parseStreamingCodeFence,
  parseStreamingPlainTail,
  parseStreamingStructuredTail,
  splitStreamingMarkdown,
} from "@/components/workspace/message-markdown-streaming";
import { cn } from "@/lib/utils";
import { type MessageMarkdownProps } from "./message-markdown/types";
import { createMarkdownComponents } from "./message-markdown/markdown-components";
import { renderStreamingStructuredTail, renderStreamingPlainTail } from "./message-markdown/streaming-tails";
import { rehypeCollapseBreakNewlines } from "./message-markdown/rehype-collapse-break-newlines";
import { StableMarkdownFragment } from "./message-markdown/stable-fragment";
import { markdownBlockKey } from "./message-markdown/block-key";

export const MessageMarkdown = memo(function MessageMarkdown({
  className,
  content,
  interrupted = false,
  streaming = false,
}: MessageMarkdownProps) {
  const { t } = useTranslation("workspace");
  const deferredContent = useDeferredValue(content);
  // 流式分支必须**立即**渲染，不能降级到 transition lane：
  // `use-typewriter` 在流式期间按 rAF（60fps）持续改写目标文本，每次都是一个
  // 紧急更新；`useDeferredValue` 每帧重新申请一个 transition lane，而 transition
  // 只在紧急队列排空时才跑 —— 连续流下永远排不到，`renderContent` 停在旧值，
  // 用户看到的就是「流式输出完成后才整体渲染」（2026-09-15 复现：perfmark 连续流，
  // 见 `e2e/zz-perf-probe.manual.ts`）。
  // 非流式路径保留延迟：历史回填 / 消息改写是低频大块替换，降级能避免阻塞输入，
  // 且此时没有连续紧急更新，不存在饿死。
  const renderContent = streaming ? content : deferredContent;
  const markdownComponents = useMemo(
    () => createMarkdownComponents(streaming),
    [streaming],
  );
  const streamingParts = useMemo(
    () =>
      streaming
        ? splitStreamingMarkdown(renderContent)
        : {
            stableContent: renderContent,
            stableBlocks: [],
            stableEndOffset: 0,
            tailContent: "",
            tailMode: null,
            tailBlocks: [],
            totalOffset: renderContent.length,
          },
    [renderContent, streaming],
  );
  const renderedStableContent = streamingParts.stableContent;
  // 不稳定区可能含多个块（尾块 + 它可能继续吞并的空行续块）。前面的块内容已定稿，
  // 按绝对 offset 各自成片（key 与冻结块同源，跨边界移动时不 remount）；只有尾块
  // 需要走流式路径（code fence / 结构块 / plain）。
  const leadingTailBlocks = streamingParts.tailBlocks.slice(0, -1);
  const lastTailBlock =
    streamingParts.tailBlocks[streamingParts.tailBlocks.length - 1];
  const tailBaseOffset = streamingParts.stableEndOffset;
  const lastTailContent = lastTailBlock
    ? streamingParts.tailContent.slice(lastTailBlock.start - tailBaseOffset)
    : "";
  // 冻结块与不稳定区首块拼成同一个 keyed 列表：块从「尾块前块」滑进「冻结前缀」时
  // key 与元素类型都不变，React 因此复用 fiber（memo 直接跳过重渲染），不会重解析。
  const settledBlocks = [...streamingParts.stableBlocks, ...leadingTailBlocks];
  const tailSpacing = settledBlocks.length > 0 ? "mt-3" : undefined;
  const settledBlockContent = (block: { start: number; end: number }) =>
    block.end <= tailBaseOffset
      ? renderedStableContent.slice(block.start, block.end)
      : streamingParts.tailContent.slice(
          block.start - tailBaseOffset,
          block.end - tailBaseOffset,
        );
  // key 只认块内容（见 block-key.ts）：块 offset 随解析重排漂移、冻结代因尾部
  // 回退而递增，两者都会让整段前缀在同一帧集体 remount。
  const settledOccurrences = new Map<string, number>();
  const settledEntries = settledBlocks.map((block) => {
    const blockContent = settledBlockContent(block);
    const occurrence = settledOccurrences.get(blockContent) ?? 0;
    settledOccurrences.set(blockContent, occurrence + 1);
    return { key: markdownBlockKey(blockContent, occurrence), content: blockContent };
  });
  const activeStreamingCodeFence = useMemo(
    () =>
      streaming && streamingParts.tailMode === "markdown"
        ? parseStreamingCodeFence(lastTailContent)
        : null,
    [lastTailContent, streaming, streamingParts.tailMode],
  );
  const activeStreamingPlainTail = useMemo(
    () =>
      streaming && streamingParts.tailMode === "plain"
        ? parseStreamingPlainTail(lastTailContent)
        : null,
    [lastTailContent, streaming, streamingParts.tailMode],
  );
  const activeStreamingStructuredTail = useMemo(
    () =>
      streaming &&
      streamingParts.tailMode === "markdown" &&
      !activeStreamingCodeFence
        ? parseStreamingStructuredTail(lastTailContent)
        : null,
    [
      activeStreamingCodeFence,
      lastTailContent,
      streaming,
      streamingParts.tailMode,
    ],
  );

  return (
    <div
      aria-busy={streaming ? "true" : undefined}
      className={cn(
        "app-chat-copy min-w-0 text-foreground",
        className,
      )}
    >
      {streaming ? (
        // 前缀按块渲染：key = 块内容（跨 offset 漂移 / 冻结边界移动都稳定），
        // 块内容本身逐字不变，因此 memo 让每块只解析一次，而不是随每个 chunk
        // 重解析整段前缀；块真的被改写时只有该块换 key。
        settledEntries.map((entry) => (
          <StableMarkdownFragment
            key={entry.key}
            components={markdownComponents}
            content={entry.content}
          />
        ))
      ) : renderedStableContent ? (
        <StableMarkdownFragment
          components={markdownComponents}
          content={renderedStableContent}
        />
      ) : null}

      {lastTailContent ? (
        activeStreamingCodeFence ? (
          <CodeBlock
            className={tailSpacing}
            code={activeStreamingCodeFence.stableCode}
            language={activeStreamingCodeFence.language}
            partialLine={activeStreamingCodeFence.partialLine || undefined}
            streaming
            title={
              activeStreamingCodeFence.info
                ? `Streaming ${activeStreamingCodeFence.info}`
                : "Streaming code"
            }
          />
        ) : activeStreamingStructuredTail ? (
          renderStreamingStructuredTail(
            activeStreamingStructuredTail,
            tailSpacing,
          )
        ) : activeStreamingPlainTail ? (
          renderStreamingPlainTail(activeStreamingPlainTail, tailSpacing)
        ) : streamingParts.tailMode === "markdown" ? (
          <div className={tailSpacing}>
            <ReactMarkdown
              components={markdownComponents}
              rehypePlugins={[rehypeCollapseBreakNewlines]}
              remarkPlugins={[remarkGfm, remarkBreaks]}
            >
              {normalizeMarkdown(lastTailContent, streaming)}
            </ReactMarkdown>
          </div>
        ) : (
          renderStreamingPlainTail(
            {
              activeText: lastTailContent,
              mode: "sentence",
              stableText: "",
            },
            tailSpacing,
          )
        )
      ) : null}

      {!streaming && interrupted ? (
        <div
          aria-label={t("panels.messages.markdown.stoppedAriaLabel")}
          className="mt-4 inline-flex items-center gap-1.5 rounded-full border border-border bg-surface-solid px-2.5 py-1 app-text-10 font-medium uppercase tracking-[0.12em] text-muted-foreground"
          role="status"
        >
          <SquareIcon aria-hidden="true" size={11} />
          {t("panels.messages.markdown.stopped")}
        </div>
      ) : null}
    </div>
  );
});

