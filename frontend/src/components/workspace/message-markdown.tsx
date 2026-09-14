// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 入口导出面保持不变（MessageMarkdown）；内部实现见 ./message-markdown/ 各域模块。

import {
  useDeferredValue,
  memo,
  useMemo,
  useState,
} from "react";
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

export const MessageMarkdown = memo(function MessageMarkdown({
  className,
  content,
  interrupted = false,
  streaming = false,
}: MessageMarkdownProps) {
  const { t } = useTranslation("workspace");
  const deferredContent = useDeferredValue(content);
  const renderContent = streaming ? deferredContent : content;
  // generation：追加输入保持不变，非追加输入（重新生成 / 内容被改写）递增，
  // 让下游按代丢弃冻结缓存（渲染 key 里带 generation，React 因此 remount 而不是
  // 复用上一代的块）。使用 render 期间 state 调整（官方 "storing information from
  // previous renders" 模式）而非 ref，以满足 react-hooks/refs 规则。
  const [freezeState, setFreezeState] = useState({
    content: "",
    generation: 0,
  });
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
  if (!streaming && freezeState.content) {
    // 结算后清账：下一次流式 / 重新生成从 generation 0 重新起算。
    setFreezeState({ content: "", generation: 0 });
  } else if (streaming && freezeState.content !== streamingParts.stableContent) {
    const appended = streamingParts.stableContent.startsWith(
      freezeState.content,
    );
    setFreezeState({
      content: streamingParts.stableContent,
      generation: appended ? freezeState.generation : freezeState.generation + 1,
    });
  }
  const generation = freezeState.generation;
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
        // 前缀按块渲染：key = 块在全文中的绝对起始 offset（跨冻结边界稳定），
        // 块内容本身逐字不变，因此 memo 让每块只解析一次，而不是随每个 chunk
        // 重解析整段前缀。
        settledBlocks.map((block) => (
          <StableMarkdownFragment
            key={`${generation}:${block.start}`}
            components={markdownComponents}
            content={settledBlockContent(block)}
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

