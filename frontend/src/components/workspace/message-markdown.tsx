import {
  useDeferredValue,
  isValidElement,
  memo,
  useMemo,
  type ReactNode,
} from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";

import { CodeBlock } from "@/components/ui/code-block";
import {
  normalizeMarkdown,
  parseStreamingCodeFence,
  parseStreamingPlainTail,
  parseStreamingStructuredTail,
  splitStreamingMarkdown,
  type StreamingPlainTail,
  type StreamingStructuredTail,
} from "@/components/workspace/message-markdown-streaming";
import { cn } from "@/lib/utils";

type MessageMarkdownProps = {
  className?: string;
  content: string;
  streaming?: boolean;
};

const LINK_CLASS_NAME =
  "font-medium text-[var(--accent-secondary)] underline decoration-[var(--accent-secondary)]/35 underline-offset-4 transition hover:text-[var(--foreground)] hover:decoration-[var(--accent-secondary)]";
const INLINE_CODE_CLASS_NAME =
  "app-inline-mono rounded-md border border-[var(--border)] bg-[var(--surface-solid)] px-1.5 py-0.5 text-[0.95em] text-[var(--foreground)]";

function collectTextContent(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") {
    return String(node);
  }

  if (Array.isArray(node)) {
    return node.map((item) => collectTextContent(item)).join("");
  }

  if (node === null || node === undefined || typeof node === "boolean") {
    return "";
  }

  if (isValidElement<{ children?: ReactNode }>(node)) {
    return collectTextContent(node.props.children);
  }

  return "";
}

function getCodeLanguage(className?: string) {
  const match = /language-([A-Za-z0-9_-]+)/.exec(className ?? "");
  return match?.[1] ?? null;
}

function isInternalHref(href: string) {
  return /^(#|\/(?!\/)|\.\.?\/)/.test(href);
}

function renderMarkdownLink(children: ReactNode, href?: string) {
  const target =
    href && !isInternalHref(href) ? "_blank" : undefined;
  const rel = target ? "noreferrer noopener" : undefined;

  return (
    <a
      className={LINK_CLASS_NAME}
      href={href}
      rel={rel}
      target={target}
    >
      {children}
    </a>
  );
}

function createMarkdownComponents(streaming: boolean): Components {
  return {
    a: ({ children, href }) => renderMarkdownLink(children, href),
    blockquote: ({ children }) => (
      <blockquote className="my-4 rounded-r-[0.8rem] border-l-2 border-[var(--accent-secondary)]/45 bg-[var(--surface-solid)] px-4 py-2.5 text-[var(--muted-foreground)]">
        {children}
      </blockquote>
    ),
    code: ({ children, className }) => {
      const language = getCodeLanguage(className);
      if (language) {
        return (
          <CodeBlock
            className="my-4"
            collapsible
            code={collectTextContent(children).replace(/\n$/, "")}
            language={language}
            streaming={streaming}
          />
        );
      }

      return (
        <code className={INLINE_CODE_CLASS_NAME}>
          {children}
        </code>
      );
    },
    h1: ({ children }) => (
      <h1 className="mb-3 mt-5 text-[1.45em] font-semibold tracking-[-0.02em] text-[var(--foreground)] first:mt-0">
        {children}
      </h1>
    ),
    h2: ({ children }) => (
      <h2 className="mb-3 mt-5 text-[1.28em] font-semibold tracking-[-0.02em] text-[var(--foreground)] first:mt-0">
        {children}
      </h2>
    ),
    h3: ({ children }) => (
      <h3 className="mb-2.5 mt-4 text-[1.14em] font-semibold text-[var(--foreground)] first:mt-0">
        {children}
      </h3>
    ),
    hr: () => <hr className="my-4 border-0 border-t border-[var(--border)]" />,
    img: ({ alt, src }) => (
      <img
        alt={alt ?? ""}
        className="my-4 max-h-[24rem] max-w-full rounded-[0.8rem] border border-[var(--border)] object-contain"
        loading="lazy"
        src={src}
      />
    ),
    input: ({ checked, type }) =>
      type === "checkbox" ? (
        <input
          checked={checked}
          className="mr-2 size-3.5 accent-[var(--accent-secondary)]"
          disabled
          type="checkbox"
        />
      ) : null,
    li: ({ children }) => (
      <li className="break-words pl-1 [&>p]:my-0">{children}</li>
    ),
    ol: ({ children }) => (
      <ol className="my-3 list-decimal space-y-2 pl-6 marker:text-[var(--muted-foreground)]">
        {children}
      </ol>
    ),
    p: ({ children }) => (
      <p className="my-3 whitespace-pre-wrap break-words text-[var(--foreground)] first:mt-0 last:mb-0">
        {children}
      </p>
    ),
    pre: ({ children }) => <>{children}</>,
    table: ({ children }) => (
      <div className="my-4 overflow-x-auto rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)]">
        <table className="min-w-full border-collapse text-left app-text-13">
          {children}
        </table>
      </div>
    ),
    td: ({ children }) => (
      <td className="border-t border-[var(--border)] px-3 py-2.5 align-top text-[var(--foreground)]">
        {children}
      </td>
    ),
    th: ({ children }) => (
      <th className="bg-[var(--surface-softer)] px-3 py-2.5 font-semibold text-[var(--foreground)]">
        {children}
      </th>
    ),
    ul: ({ children }) => (
      <ul className="my-3 list-disc space-y-2 pl-6 marker:text-[var(--accent-secondary)]">
        {children}
      </ul>
    ),
  };
}

const inlineMarkdownComponents: Components = {
  a: ({ children, href }) => renderMarkdownLink(children, href),
  code: ({ children }) => (
    <code className={INLINE_CODE_CLASS_NAME}>
      {children}
    </code>
  ),
  p: ({ children }) => <>{children}</>,
};

function areStringArraysEqual(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

const InlineMarkdown = memo(function InlineMarkdown({
  content,
}: {
  content: string;
}) {
  return (
    <ReactMarkdown
      components={inlineMarkdownComponents}
      remarkPlugins={[remarkGfm, remarkBreaks]}
    >
      {content}
    </ReactMarkdown>
  );
});

function alignmentToClassName(alignment: "left" | "center" | "right" | null) {
  if (alignment === "center") {
    return "text-center";
  }
  if (alignment === "right") {
    return "text-right";
  }
  return "text-left";
}

const StreamingBlockquoteParagraph = memo(function StreamingBlockquoteParagraph({
  active,
  className,
  content,
}: {
  active: boolean;
  className?: string;
  content: string;
}) {
  return (
    <p
      className={cn(
        "whitespace-pre-wrap break-words text-[var(--muted-foreground)]",
        className,
      )}
      aria-atomic={active ? "true" : undefined}
      aria-live={active ? "polite" : "off"}
      data-streaming-active={active ? "true" : undefined}
    >
      <InlineMarkdown content={content} />
    </p>
  );
});

const StreamingListItem = memo(function StreamingListItem({
  active,
  content,
}: {
  active: boolean;
  content: string;
}) {
  return (
    <li
      aria-atomic={active ? "true" : undefined}
      aria-live={active ? "polite" : "off"}
      className="break-words pl-1"
      data-streaming-active={active ? "true" : undefined}
    >
      <InlineMarkdown content={content} />
    </li>
  );
});

const StreamingTableHeaderCell = memo(function StreamingTableHeaderCell({
  alignment,
  content,
}: {
  alignment: "left" | "center" | "right" | null;
  content: string;
}) {
  return (
    <th
      className={cn(
        "bg-[var(--surface-softer)] px-3 py-2.5 font-semibold text-[var(--foreground)]",
        alignmentToClassName(alignment),
      )}
    >
      <InlineMarkdown content={content} />
    </th>
  );
});

const StreamingTableRow = memo(
  function StreamingTableRow({
    active,
    alignments,
    cells,
  }: {
    active: boolean;
    alignments: Array<"left" | "center" | "right" | null>;
    cells: string[];
  }) {
    return (
      <tr
        aria-atomic={active ? "true" : undefined}
        aria-live={active ? "polite" : "off"}
        data-streaming-active={active ? "true" : undefined}
      >
        {cells.map((cell, cellIndex) => (
          <td
            key={`streaming-table-cell-${cellIndex}-${cell}`}
            className={cn(
              "border-t border-[var(--border)] px-3 py-2.5 align-top text-[var(--foreground)]",
              alignmentToClassName(alignments[cellIndex] ?? null),
            )}
          >
            <InlineMarkdown content={cell} />
          </td>
        ))}
      </tr>
    );
  },
  (previousProps, nextProps) =>
    previousProps.active === nextProps.active &&
    areStringArraysEqual(previousProps.cells, nextProps.cells) &&
    previousProps.alignments.length === nextProps.alignments.length &&
    previousProps.alignments.every(
      (value, index) => value === nextProps.alignments[index],
    ),
);

const StreamingPlainFragment = memo(function StreamingPlainFragment({
  active,
  content,
}: {
  active: boolean;
  content: string;
}) {
  if (!content) {
    return null;
  }

  return (
    <span
      aria-atomic={active ? "true" : undefined}
      aria-live={active ? "polite" : "off"}
      data-streaming-active={active ? "true" : undefined}
    >
      {content}
    </span>
  );
});

function renderStreamingStructuredTail(
  tail: StreamingStructuredTail,
  className?: string,
) {
  if (tail.kind === "blockquote") {
    return (
      <blockquote
        className={cn(
          "rounded-r-[0.8rem] border-l-2 border-[var(--accent-secondary)]/45 bg-[var(--surface-solid)] px-4 py-2.5 text-[var(--muted-foreground)]",
          className,
        )}
      >
        {tail.paragraphs.map((paragraph, index) => (
          <StreamingBlockquoteParagraph
            key={`streaming-quote-${index}`}
            active={index === tail.paragraphs.length - 1}
            className={index > 0 ? "mt-3" : undefined}
            content={paragraph}
          />
        ))}
      </blockquote>
    );
  }

  if (tail.kind === "list") {
    const ListTag = tail.ordered ? "ol" : "ul";
    return (
      <ListTag
        className={cn(
          tail.ordered
            ? "list-decimal space-y-2 pl-6 marker:text-[var(--muted-foreground)]"
            : "list-disc space-y-2 pl-6 marker:text-[var(--accent-secondary)]",
          className,
        )}
        start={tail.ordered && tail.start ? tail.start : undefined}
      >
        {tail.items.map((item, index) => (
          <StreamingListItem
            key={`streaming-list-${index}`}
            active={index === tail.items.length - 1}
            content={item}
          />
        ))}
      </ListTag>
    );
  }

  return (
    <div
      className={cn(
        "overflow-x-auto rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)]",
        className,
      )}
    >
      <table className="min-w-full border-collapse text-left app-text-13">
        <thead>
          <tr>
            {tail.headers.map((header, index) => (
              <StreamingTableHeaderCell
                key={`streaming-table-header-${index}`}
                alignment={tail.alignments[index] ?? null}
                content={header}
              />
            ))}
          </tr>
        </thead>
        <tbody>
          {tail.rows.map((row, rowIndex) => (
            <StreamingTableRow
              key={`streaming-table-row-${rowIndex}`}
              active={rowIndex === tail.rows.length - 1}
              alignments={tail.alignments}
              cells={row}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function renderStreamingPlainTail(
  tail: StreamingPlainTail,
  className?: string,
) {
  return (
    <p
      className={cn(
        "whitespace-pre-wrap break-words text-[var(--foreground)]",
        className,
      )}
      data-streaming-mode={tail.mode}
    >
      <StreamingPlainFragment active={false} content={tail.stableText} />
      <StreamingPlainFragment active content={tail.activeText} />
    </p>
  );
}

export const MessageMarkdown = memo(function MessageMarkdown({
  className,
  content,
  streaming = false,
}: MessageMarkdownProps) {
  const deferredContent = useDeferredValue(content);
  const renderContent = streaming ? deferredContent : content;
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
        "app-chat-copy min-w-0 text-[var(--foreground)]",
        className,
      )}
    >
      {streamingParts.stableContent ? (
        <ReactMarkdown
          components={markdownComponents}
          remarkPlugins={[remarkGfm, remarkBreaks]}
        >
          {normalizeMarkdown(streamingParts.stableContent, false)}
        </ReactMarkdown>
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
    </div>
  );
});
