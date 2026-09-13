export type StreamingMarkdownTailMode = "plain" | "markdown" | null;

export type StreamingMarkdownParts = {
  stableContent: string;
  tailContent: string;
  tailMode: StreamingMarkdownTailMode;
  /** 冻结切割点（= 尾部起点）在全文中的绝对 offset，`stableContent` 即 `[0, stableEndOffset)` 切片。 */
  stableEndOffset: number;
  /** 输入（已归一化行尾）总长度，供渲染层生成绝对 offset key。 */
  totalOffset: number;
  /** 冻结前缀的块切片（绝对 offset），渲染层按 `start` 生成跨边界稳定的 key。 */
  stableBlocks: MarkdownBlockSpan[];
  /** 不稳定尾部的块切片；最后一个块的 `start` 即尾部元素的 key。 */
  tailBlocks: MarkdownBlockSpan[];
};

export type StreamingCodeFence = {
  code: string;
  info: string;
  language: string;
  marker: string;
  /** 第二前沿：已完结的整行代码（保持 `\n` 结尾，可能为空串）。 */
  stableCode: string;
  /** 第二前沿：仍在增长的 partial 行（最后一个 `\n` 之后的文本，可能为空串）。 */
  partialLine: string;
};

/**
 * 前缀冻结时允许不稳定的尾部块数：除尾部这么多块外全部冻结。
 *
 * 取 2 而不是 1，是因为一个「看起来已结束」的块仍可能被后一个块改写容器归属：
 * 松散列表（`- a` 空行 `- b`）、多段引用、跨空行的表格都跨空行、却属于同一个解析块，
 * 提前冻结会把它们渲染成两个独立块，settled 后又要跳变。
 */
export const UNSTABLE_TAIL_BLOCKS = 2;

export type MarkdownBlockSpan = {
  /** 块在全文中的绝对起始 offset（渲染 key 的来源，跨冻结边界稳定）。 */
  start: number;
  /** 下一块的起始 offset：`[start, end)` 含块文本与尾随分隔空白，切片逐字一致且无空洞。 */
  end: number;
  /** 容器上下文，用于把跨空行的松散列表 / 多段引用合并为同一个块。 */
  container: "list" | "quote" | "plain";
};

type MarkdownBlockDraft = MarkdownBlockSpan & {
  lastLineEnd: number;
};

function classifyContainerLine(
  line: string,
  current: MarkdownBlockDraft | null,
): MarkdownBlockSpan["container"] {
  if (/^[ \t]{0,3}> ?/.test(line)) {
    return "quote";
  }
  if (/^[ \t]{0,3}(?:[-*+] |\d+[.)] )/.test(line)) {
    return "list";
  }
  // 缩进续行沿用当前容器（列表项换行 / 引用内部换行）。
  if (current && current.container !== "plain" && /^[ \t]{2,}\S/.test(line)) {
    return current.container;
  }
  return "plain";
}

/**
 * 以空行切分顶层块，并把同一容器上下文（列表 / 引用）跨空行合并为一块。
 * 切割点取块的 `end`（下一块起点），因此任意 `content.slice(0, end)` 都是逐字一致的前缀。
 */
export function scanMarkdownBlocks(content: string): MarkdownBlockSpan[] {
  const blocks: MarkdownBlockSpan[] = [];
  if (!content) {
    return blocks;
  }

  let offset = 0;
  let current: MarkdownBlockDraft | null = null;
  let sawBlankLine = false;

  const closeCurrent = (end: number) => {
    if (current) {
      blocks.push({
        container: current.container,
        end,
        start: current.start,
      });
    }
    current = null;
  };

  for (const line of content.split("\n")) {
    const lineStart = offset;
    const lineEnd = offset + line.length;
    offset = lineEnd + 1;

    if (!line.trim()) {
      sawBlankLine = true;
      continue;
    }

    const container = classifyContainerLine(line, current);
    if (!current) {
      current = {
        container,
        end: lineEnd,
        lastLineEnd: lineEnd,
        start: lineStart,
      };
      sawBlankLine = false;
      continue;
    }

    const continuesContainer =
      sawBlankLine && container !== "plain" && container === current.container;
    if (!sawBlankLine || continuesContainer) {
      current.end = lineEnd;
      current.lastLineEnd = lineEnd;
      current.container = continuesContainer ? current.container : container;
      sawBlankLine = false;
      continue;
    }

    closeCurrent(lineStart);
    current = {
      container,
      end: lineEnd,
      lastLineEnd: lineEnd,
      start: lineStart,
    };
    sawBlankLine = false;
  }

  closeCurrent(content.length);
  return blocks;
}

export type StreamingStructuredTail =
  | {
      kind: "blockquote";
      paragraphs: string[];
    }
  | {
      kind: "list";
      items: string[];
      ordered: boolean;
      start: number | null;
    }
  | {
      alignments: Array<"left" | "center" | "right" | null>;
      headers: string[];
      kind: "table";
      rows: string[][];
    };

export type StreamingPlainTail = {
  activeText: string;
  mode: "line" | "sentence";
  stableText: string;
};

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

type FenceState = {
  index: number;
  marker: string;
};

export function normalizeMarkdownLineEndings(content: string) {
  return content.replace(/\r\n?/g, "\n");
}

function normalizeFenceLanguage(info: string) {
  const firstToken = info.trim().match(/^([^\s{]+)/)?.[1];
  return firstToken?.trim() || "text";
}

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
  return null;
}

function isStructuredMarkdownBlock(content: string) {
  return STRUCTURED_BLOCK_PATTERNS.some((pattern) => pattern.test(content));
}

function findLastUnclosedFenceStart(content: string) {
  const fencePattern = /^[ \t]{0,3}(`{3,}|~{3,})[^\n]*$/gm;
  let activeFence: FenceState | null = null;

  for (const match of content.matchAll(fencePattern)) {
    const marker = match[1];
    const index = match.index ?? 0;
    if (!activeFence) {
      activeFence = { index, marker };
      continue;
    }

    if (
      marker[0] === activeFence.marker[0] &&
      marker.length >= activeFence.marker.length
    ) {
      activeFence = null;
      continue;
    }

    activeFence = { index, marker };
  }

  return activeFence?.index ?? null;
}

export function parseStreamingCodeFence(content: string): StreamingCodeFence | null {
  const normalized = normalizeMarkdownLineEndings(content);
  if (findLastUnclosedFenceStart(normalized) !== 0) {
    return null;
  }

  const match = /^(?:[ \t]{0,3})(`{3,}|~{3,})([^\n]*)(?:\n([\s\S]*))?$/.exec(
    normalized,
  );
  if (!match) {
    return null;
  }

  const marker = match[1];
  const info = (match[2] ?? "").trim();
  const body = match[3] ?? "";
  const code = body.replace(/\n$/, "");
  // 第二前沿：整行代码与仍在增长的 partial 行分开——半行代码交给 Prism 会得到
  // 错误 token（例如还没闭合的字符串会把后续内容整体染色），因此 partial 行按
  // 纯文本渲染，整行部分才是可缓存的高亮输入。
  const partialLine = body.endsWith("\n")
    ? ""
    : body.slice(body.lastIndexOf("\n") + 1);
  const stableCode = body.slice(0, body.length - partialLine.length);
  return {
    code,
    info,
    language: normalizeFenceLanguage(info),
    marker,
    partialLine,
    stableCode,
  };
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

export function normalizeMarkdown(content: string, streaming: boolean) {
  const normalized = normalizeMarkdownLineEndings(content);
  if (!streaming) {
    return normalized;
  }

  const unclosedFenceIndex = findLastUnclosedFenceStart(normalized);
  if (unclosedFenceIndex === null) {
    return normalized;
  }

  const markerMatch = /^[ \t]{0,3}(`{3,}|~{3,})/m.exec(
    normalized.slice(unclosedFenceIndex),
  );
  const closingMarker = markerMatch?.[1] ?? "```";
  return `${normalized}\n${closingMarker}`;
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
