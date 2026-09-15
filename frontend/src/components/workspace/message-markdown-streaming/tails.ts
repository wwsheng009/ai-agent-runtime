import { scanMarkdownBlocks, UNSTABLE_TAIL_BLOCKS } from "./blocks";
import { findLastUnclosedFenceStart, normalizeMarkdownLineEndings } from "./fence";
import type { StreamingMarkdownParts, StreamingPlainTail, StreamingStructuredTail } from "./types";

const EMPTY_STREAMING_MARKDOWN_PARTS: StreamingMarkdownParts = {
  stableContent: "",
  tailContent: "",
  tailMode: null,
  stableEndOffset: 0,
  totalOffset: 0,
  stableBlocks: [],
  tailBlocks: [],
};

const STRUCTURED_BLOCK_PATTERNS = [
  /^[ \t]{0,3}(?:[-*+] |\d+\. )/m,
  /^[ \t]{0,3}> /m,
  /^[ \t]{0,3}#{1,6}\s/m,
  /^[ \t]{0,3}(?:`{3,}|~{3,})/m,
  /^[ \t]{0,3}\|.*\|/m,
  /^[ \t]{0,3}(?:-{3,}|\*{3,}|_{3,})\s*$/m,
];

function splitTableRow(line: string) {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function parseTableAlignmentCell(
  cell: string,
): "left" | "center" | "right" | null {
  if (!/^:?-{3,}:?$/.test(cell.trim())) {
    return null;
  }
  if (cell.startsWith(":") && cell.endsWith(":")) {
    return "center";
  }
  if (cell.endsWith(":")) {
    return "right";
  }
  if (cell.startsWith(":")) {
    return "left";
  }
  // 无冒号的 `---` 是合法 GFM 分隔行（对齐 = 默认左对齐），必须返回 "left"。
  // 此前这里返回 null，会让 `isAlignmentRowValid` 判 false，把**最常见**的表格
  // 写法 `| --- | --- |` 整体踢出结构化流式路径，回落到 ReactMarkdown 每帧重新
  // 解析整块：实测同一场景 ScriptDuration 10.7s / 16s，比结构化路径贵 45%。
  // 现在 null 只表示「这个单元格不是分隔行」（如 `| abc |`、`| -- |`）。
  return "left";
}

function isStructuredMarkdownBlock(content: string) {
  return STRUCTURED_BLOCK_PATTERNS.some((pattern) => pattern.test(content));
}

export function parseStreamingStructuredTail(
  content: string,
): StreamingStructuredTail | null {
  const normalized = normalizeMarkdownLineEndings(content).trim();
  if (!normalized) {
    return null;
  }

  const blockquoteLines = normalized.split("\n");
  if (
    blockquoteLines.every((line) => !line.trim() || /^[ \t]{0,3}> ?/.test(line))
  ) {
    const paragraphs = blockquoteLines
      .map((line) => line.replace(/^[ \t]{0,3}> ?/, ""))
      .join("\n")
      .split(/\n{2,}/)
      .map((paragraph) => paragraph.trim())
      .filter(Boolean);
    if (paragraphs.length > 0) {
      return {
        kind: "blockquote",
        paragraphs,
      };
    }
  }

  const listLines = normalized.split("\n");
  const listItems: string[] = [];
  let listOrdered: boolean | null = null;
  let orderedStart: number | null = null;
  let currentItemIndex = -1;

  for (const line of listLines) {
    if (!line.trim()) {
      if (currentItemIndex >= 0) {
        listItems[currentItemIndex] = `${listItems[currentItemIndex]}\n`;
        continue;
      }
      currentItemIndex = -1;
      break;
    }

    const unorderedMatch = /^[ \t]{0,3}[-*+] +(.*)$/.exec(line);
    const orderedMatch = /^[ \t]{0,3}(\d+)\. +(.*)$/.exec(line);
    if (unorderedMatch || orderedMatch) {
      const isOrdered = Boolean(orderedMatch);
      if (listOrdered === null) {
        listOrdered = isOrdered;
      } else if (listOrdered !== isOrdered) {
        currentItemIndex = -1;
        break;
      }

      currentItemIndex += 1;
      if (orderedMatch && orderedStart === null) {
        orderedStart = Number(orderedMatch[1]);
      }
      listItems.push((unorderedMatch?.[1] ?? orderedMatch?.[2] ?? "").trim());
      continue;
    }

    if (/^[ \t]{2,}\S/.test(line) && currentItemIndex >= 0) {
      listItems[currentItemIndex] = `${listItems[currentItemIndex]}\n${line.trim()}`;
      continue;
    }

    currentItemIndex = -1;
    break;
  }

  if (listItems.length > 0 && currentItemIndex === listItems.length - 1) {
    return {
      kind: "list",
      items: listItems,
      ordered: Boolean(listOrdered),
      start: listOrdered ? orderedStart : null,
    };
  }

  const tableLines = normalized.split("\n");
  if (tableLines.length >= 2 && tableLines.every((line) => line.includes("|"))) {
    const headers = splitTableRow(tableLines[0]);
    const alignments = splitTableRow(tableLines[1]).map(parseTableAlignmentCell);
    const rows = tableLines.slice(2).map(splitTableRow);
    const isAlignmentRowValid =
      alignments.length === headers.length &&
      alignments.every((alignment) => alignment !== null);
    const areRowsValid =
      rows.length > 0 &&
      rows.every((row) => row.length === headers.length);

    if (headers.length > 0 && isAlignmentRowValid && areRowsValid) {
      return {
        kind: "table",
        headers,
        alignments,
        rows,
      };
    }
  }

  return null;
}

export function parseStreamingPlainTail(content: string): StreamingPlainTail | null {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!normalized) {
    return null;
  }

  if (normalized.includes("\n")) {
    const lastLineBreak = normalized.lastIndexOf("\n");
    return {
      activeText: normalized.slice(lastLineBreak + 1),
      mode: "line",
      stableText: normalized.slice(0, lastLineBreak + 1),
    };
  }

  const sentenceBoundaryPattern = /[.!?。！？](?:["')\]]+)?\s+/g;
  let sentenceBoundaryIndex = -1;
  for (const match of normalized.matchAll(sentenceBoundaryPattern)) {
    sentenceBoundaryIndex = (match.index ?? 0) + match[0].length;
  }

  if (sentenceBoundaryIndex > 0 && sentenceBoundaryIndex < normalized.length) {
    return {
      activeText: normalized.slice(sentenceBoundaryIndex),
      mode: "sentence",
      stableText: normalized.slice(0, sentenceBoundaryIndex),
    };
  }

  const fallbackWordBoundary = normalized.lastIndexOf(" ");
  if (
    fallbackWordBoundary > 0 &&
    fallbackWordBoundary < normalized.length - 1 &&
    normalized.length >= 48
  ) {
    return {
      activeText: normalized.slice(fallbackWordBoundary + 1),
      mode: "sentence",
      stableText: `${normalized.slice(0, fallbackWordBoundary + 1)}`,
    };
  }

  return {
    activeText: normalized,
    mode: "sentence",
    stableText: "",
  };
}

export function splitStreamingMarkdown(content: string): StreamingMarkdownParts {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!normalized.trim()) {
    return EMPTY_STREAMING_MARKDOWN_PARTS;
  }

  // 未闭合顶层 fence：切割点取 fence 起点（fence 之前的内容都已成块、不会再变），
  // fence 内部再由 `parseStreamingCodeFence` 走「最后完整行 + partial 行」第二前沿。
  const unclosedFenceIndex = findLastUnclosedFenceStart(normalized);
  if (unclosedFenceIndex !== null) {
    const blocks = scanMarkdownBlocks(normalized);
    // fence 可以紧跟在段落行之后（CommonMark 允许 fence 中断段落），此时 fence 起点
    // 落在某个块内部：把该块按 fence 起点截断，避免这段文本既不在冻结块里、也不在
    // 尾块里而被丢掉。
    const stableBlocks = blocks
      .filter((block) => block.start < unclosedFenceIndex)
      .map((block) =>
        block.end <= unclosedFenceIndex
          ? block
          : { ...block, end: unclosedFenceIndex },
      );
    return {
      stableContent: normalized.slice(0, unclosedFenceIndex),
      stableBlocks,
      stableEndOffset: unclosedFenceIndex,
      tailContent: normalized.slice(unclosedFenceIndex),
      tailBlocks: [
        {
          container: "plain",
          end: normalized.length,
          start: unclosedFenceIndex,
        },
      ],
      tailMode: "markdown",
      totalOffset: normalized.length,
    };
  }

  // 除尾部 `UNSTABLE_TAIL_BLOCKS` 块外全部冻结；切割点取最后一个冻结块的 end offset，
  // 因此 `stableContent` 与 `tailContent` 是逐字一致、无空洞的两段切片。
  const blocks = scanMarkdownBlocks(normalized);
  const unstableBlockCount = Math.min(UNSTABLE_TAIL_BLOCKS, blocks.length);
  const frozenBlocks = blocks.slice(0, blocks.length - unstableBlockCount);
  const tailBlocks = blocks.slice(blocks.length - unstableBlockCount);
  const stableEndOffset = frozenBlocks.length
    ? frozenBlocks[frozenBlocks.length - 1].end
    : 0;
  const stableContent = normalized.slice(0, stableEndOffset);
  const tailContent = normalized.slice(stableEndOffset);
  // 只有一个不稳定块时保留 plain 尾部（句/行级 active 更新）；不稳定区含多个块时，
  // 尾块模式按最后一个块判定，前面的块由渲染层按绝对 offset 单独成片。
  const lastTailBlock = tailBlocks[tailBlocks.length - 1];
  const lastTailContent = lastTailBlock
    ? normalized.slice(lastTailBlock.start)
    : "";
  return {
    stableContent,
    stableBlocks: frozenBlocks,
    stableEndOffset,
    tailContent,
    tailBlocks,
    tailMode: lastTailContent
      ? isStructuredMarkdownBlock(lastTailContent)
        ? "markdown"
        : "plain"
      : null,
    totalOffset: normalized.length,
  };
}
