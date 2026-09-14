// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 流式尾块子组件（引用块段落 / 列表项 / 表头单元格 / 表格行 / 纯文本片段）

import { memo } from "react";
import ReactMarkdown from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";
import { createInlineMarkdownComponents } from "./markdown-components";
import { rehypeCollapseBreakNewlines } from "./rehype-collapse-break-newlines";

// 流式尾块按定义来自未结算内容：链接只渲染占位 <a>（不烘焙 href）、图片只渲染
// 占位 span（不发请求），settled 后由主路径 `createMarkdownComponents(false)`
// 重新解析同一段落并补齐 href/src —— 占位即自愈路径的过渡态。
const streamingInlineMarkdownComponents = createInlineMarkdownComponents(true);

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
      components={streamingInlineMarkdownComponents}
      rehypePlugins={[rehypeCollapseBreakNewlines]}
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

export const StreamingBlockquoteParagraph = memo(function StreamingBlockquoteParagraph({
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
        "whitespace-pre-wrap break-words text-muted-foreground",
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

export const StreamingListItem = memo(function StreamingListItem({
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

export const StreamingTableHeaderCell = memo(function StreamingTableHeaderCell({
  alignment,
  content,
}: {
  alignment: "left" | "center" | "right" | null;
  content: string;
}) {
  return (
    <th
      className={cn(
        "bg-surface-softer px-3 py-2.5 font-semibold text-foreground",
        alignmentToClassName(alignment),
      )}
    >
      <InlineMarkdown content={content} />
    </th>
  );
});

export const StreamingTableRow = memo(
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
              "border-t border-border px-3 py-2.5 align-top text-foreground",
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

export const StreamingPlainFragment = memo(function StreamingPlainFragment({
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
