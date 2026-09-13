// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 入口导出面保持不变（MessageMarkdown）；内部实现见 ./message-markdown/ 各域模块。

import {
  useDeferredValue,
  memo,
  useMemo,
  useState,
} from "react";
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
import { StableMarkdownFragment } from "./message-markdown/stable-fragment";

export const MessageMarkdown = memo(function MessageMarkdown({
  className,
  content,
  interrupted = false,
  streaming = false,
}: MessageMarkdownProps) {
  const deferredContent = useDeferredValue(content);
  const renderContent = streaming ? deferredContent : content;
  // 稳定区前缀缓存：新 stableContent 以已渲染内容为前缀时（追加场景），
  // 复用冻结片段并只渲染增量（delta），避免每帧重解析全部稳定文本。
  // 使用 render 期间 state 调整（官方 "storing information from previous
  // renders" 模式）而非 ref，以满足 react-hooks/refs 规则。
  const [frozenStableContent, setFrozenStableContent] = useState("");
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
            tailContent: "",
            tailMode: null,
          },
    [renderContent, streaming],
  );
  let renderedStableContent: string;
  let stableDeltaContent = "";
  if (
    streaming &&
    frozenStableContent &&
    streamingParts.stableContent.startsWith(frozenStableContent)
  ) {
    renderedStableContent = frozenStableContent;
    stableDeltaContent = streamingParts.stableContent.slice(
      frozenStableContent.length,
    );
  } else {
    renderedStableContent = streamingParts.stableContent;
    if (frozenStableContent !== streamingParts.stableContent) {
      setFrozenStableContent(streamingParts.stableContent);
    }
  }
  const activeStreamingCodeFence = useMemo(
    () =>
      streaming && streamingParts.tailMode === "markdown"
        ? parseStreamingCodeFence(streamingParts.tailContent)
        : null,
    [streaming, streamingParts.tailContent, streamingParts.tailMode],
  );
  const activeStreamingPlainTail = useMemo(
    () =>
      streaming && streamingParts.tailMode === "plain"
        ? parseStreamingPlainTail(streamingParts.tailContent)
        : null,
    [streaming, streamingParts.tailContent, streamingParts.tailMode],
  );
  const activeStreamingStructuredTail = useMemo(
    () =>
      streaming &&
      streamingParts.tailMode === "markdown" &&
      !activeStreamingCodeFence
        ? parseStreamingStructuredTail(streamingParts.tailContent)
        : null,
    [
      activeStreamingCodeFence,
      streaming,
      streamingParts.tailContent,
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
      {renderedStableContent ? (
        <StableMarkdownFragment
          components={markdownComponents}
          content={renderedStableContent}
        />
      ) : null}

      {stableDeltaContent ? (
        <div className={renderedStableContent ? "mt-3" : undefined}>
          <ReactMarkdown
            components={markdownComponents}
            remarkPlugins={[remarkGfm, remarkBreaks]}
          >
            {normalizeMarkdown(stableDeltaContent, false)}
          </ReactMarkdown>
        </div>
      ) : null}

      {streamingParts.tailContent ? (
        activeStreamingCodeFence ? (
          <CodeBlock
            className={streamingParts.stableContent ? "mt-3" : undefined}
            code={activeStreamingCodeFence.code}
            language={activeStreamingCodeFence.language}
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
            streamingParts.stableContent ? "mt-3" : undefined,
          )
        ) : activeStreamingPlainTail ? (
          renderStreamingPlainTail(
            activeStreamingPlainTail,
            streamingParts.stableContent ? "mt-3" : undefined,
          )
        ) : streamingParts.tailMode === "markdown" ? (
          <div className={streamingParts.stableContent ? "mt-3" : undefined}>
            <ReactMarkdown
              components={markdownComponents}
              remarkPlugins={[remarkGfm, remarkBreaks]}
            >
              {normalizeMarkdown(streamingParts.tailContent, streaming)}
            </ReactMarkdown>
          </div>
        ) : (
          renderStreamingPlainTail(
            {
              activeText: streamingParts.tailContent,
              mode: "sentence",
              stableText: "",
            },
            streamingParts.stableContent ? "mt-3" : undefined,
          )
        )
      ) : null}

      {!streaming && interrupted ? (
        <div
          aria-label="Response stopped"
          className="mt-4 inline-flex items-center gap-1.5 rounded-full border border-border bg-surface-solid px-2.5 py-1 app-text-10 font-medium uppercase tracking-[0.12em] text-muted-foreground"
          role="status"
        >
          <SquareIcon aria-hidden="true" size={11} />
          Stopped
        </div>
      ) : null}
    </div>
  );
});

