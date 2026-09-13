// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 流式尾块渲染（结构化引用/列表/表格，以及纯文本尾块）

import { type StreamingPlainTail, type StreamingStructuredTail } from "@/components/workspace/message-markdown-streaming";
import { cn } from "@/lib/utils";
import {
  StreamingBlockquoteParagraph,
  StreamingListItem,
  StreamingTableHeaderCell,
  StreamingTableRow,
  StreamingPlainFragment,
} from "./streaming-blocks";

export function renderStreamingStructuredTail(
  tail: StreamingStructuredTail,
  className?: string,
) {
  if (tail.kind === "blockquote") {
    return (
      <blockquote
        className={cn(
          "rounded-r-card border-l-2 border-accent-secondary/45 bg-surface-solid px-4 py-2.5 text-muted-foreground",
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
            ? "list-decimal space-y-2 pl-6 marker:text-muted-foreground"
            : "list-disc space-y-2 pl-6 marker:text-accent-secondary",
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
        "overflow-x-auto rounded-card border border-border bg-surface-solid",
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

export function renderStreamingPlainTail(
  tail: StreamingPlainTail,
  className?: string,
) {
  return (
    <p
      className={cn(
        "whitespace-pre-wrap break-words text-foreground",
        className,
      )}
      data-streaming-mode={tail.mode}
    >
      <StreamingPlainFragment active={false} content={tail.stableText} />
      <StreamingPlainFragment active content={tail.activeText} />
    </p>
  );
}
